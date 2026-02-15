package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
)

// Config holds SIP and call parameters (from CLI, env, or config files).
type Config struct {
	SipUser        string `kong:"required,help='SIP user (Zadarma ID)'"`
	SipPass        string `kong:"required,help='SIP password'"`
	SipDomain      string `kong:"required,help='SIP domain'"`
	Destination    string `kong:"required,help='Number to call'"`
	OutgoingNumber string `kong:"help='If set, P-Asserted-Identity header is set to this value'"`
	ListenAddress  string `kong:"help='HTTP server listen address'"`
}

var cli Config

// Call status values sent over WebSocket (JSON: {"status": "..."}).
const (
	statusSendingInvite  = "sending_invite"
	statusAuthenticating = "authenticating"
	statusTrying         = "trying"
	statusHangingUpTimer = "hanging_up_timer"
	statusError          = "error"
)

type callStatusMsg struct {
	Status string `json:"status"`
}

const validCallToken = "12345"

// tokenFromRequest returns the token from Authorization: Token <value> or query ?token=
func tokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if strings.HasPrefix(h, "Token ") {
			return strings.TrimSpace(h[6:])
		}
	}
	return r.URL.Query().Get("token")
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

const uiHTML = `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>Iftach</title></head>
<body>
  <div>
    <label>Token:</label>
    <input type="text" id="token" placeholder="token" />
    <button id="set">Set</button>
    <button id="clear">Clear</button>
  </div>
  <button id="open">Open</button>
  <div id="out"></div>
  <script>
    var TOKEN_KEY = 'token';
    var statusLabels = {
      sending_invite: 'Sending INVITE',
      authenticating: 'Authenticating',
      trying: 'Trying (100)',
      hanging_up_timer: 'Hanging up (12s timer)',
      error: 'Error — check the logs'
    };
    function getToken() { return localStorage.getItem(TOKEN_KEY) || ''; }
    function setToken(v) { localStorage.setItem(TOKEN_KEY, v); document.getElementById('token').value = v; }
    function syncTokenToInput() { document.getElementById('token').value = getToken(); }
    (function() {
      var params = new URLSearchParams(location.search);
      var q = params.get('token');
      if (q !== null) {
        setToken(q);
        history.replaceState({}, '', location.pathname);
      } else {
        syncTokenToInput();
      }
    })();
    document.getElementById('set').onclick = function() {
      setToken(document.getElementById('token').value);
    };
    document.getElementById('clear').onclick = function() {
      localStorage.removeItem(TOKEN_KEY);
      document.getElementById('token').value = '';
    };
    document.getElementById('open').onclick = function() {
      var out = document.getElementById('out');
      out.innerHTML = '';
      var token = getToken();
      var wsUrl = (location.protocol === 'https:' ? 'wss:' : 'ws:') + '//' + location.host + '/call';
      if (token) wsUrl += '?token=' + encodeURIComponent(token);
      var ws = new WebSocket(wsUrl);
      ws.onopen = function() {
        addStatus(out, 'Connected — call started');
      };
      ws.onmessage = function(ev) {
        try {
          var msg = JSON.parse(ev.data);
          var label = statusLabels[msg.status] || msg.status;
          addStatus(out, label);
          if (msg.status === 'error') { ws.close(); }
        } catch (e) {
          addStatus(out, 'Invalid message');
        }
      };
      ws.onerror = function() {
		addStatus(out, 'WebSocket error');
      };
      ws.onclose = function(ev) {
        if (ev.code === 4001) {
          addStatus(out, '4001: Wrong credentials — check your token and try again.');
        } else {
          addStatus(out, 'Connection closed');
        }
      };
    };
    function addStatus(container, text) {
      var p = document.createElement('p');
      p.textContent = text;
      container.appendChild(p);
    }
  </script>
</body>
</html>
`

func main() {
	kong.Parse(&cli,
		kong.Name("Iftach"),
		kong.Description("SIP client to place a call"),
		kong.DefaultEnvars("IFTACH"),
	)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Get("/ui", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(uiHTML))
	})
	r.HandleFunc("/call", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if tokenFromRequest(r) != validCallToken {
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "Wrong credentials"))
			return
		}
		// Client only reads; we only write. Stream statuses until run() exits.
		statusChan := make(chan string, 16)
		go run(&cli, statusChan)
		for s := range statusChan {
			_ = conn.WriteJSON(callStatusMsg{Status: s})
		}
	})

	srv := &http.Server{Addr: cli.ListenAddress, Handler: r}
	go func() {
		fmt.Printf("🌐 HTTP server listening on %s (WebSocket /call to start a call)\n", cli.ListenAddress)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "server: %v\n", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	<-ctx.Done()
	stop()
	fmt.Println("\n🛑 Shutting down server...")
	_ = srv.Shutdown(context.Background())
}

// discoverPublicIP returns this host's public IPv4/IPv6 by querying well-known
// open services. Tries multiple endpoints and returns the first successful result.
func discoverPublicIP(ctx context.Context) (string, error) {
	// Services that return plain-text IP (no API key). Try in order.
	endpoints := []string{
		"https://api.ipify.org",
		"https://icanhazip.com",
		"https://ifconfig.me/ip",
	}
	client := &http.Client{Timeout: 8 * time.Second}

	for _, url := range endpoints {
		fmt.Printf("   Checking public IP via %s ... ", url)
		ip, err := fetchPublicIPFrom(ctx, client, url)
		if err != nil {
			fmt.Printf("failed: %v\n", err)
			continue
		}
		ip = strings.TrimSpace(ip)
		if ip == "" {
			fmt.Println("empty response")
			continue
		}
		fmt.Printf("ok → %s\n", ip)
		return ip, nil
	}

	return "", fmt.Errorf("all %d endpoints failed", len(endpoints))
}

func fetchPublicIPFrom(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func run(cfg *Config, statusChan chan<- string) {
	defer func() {
		if statusChan != nil {
			close(statusChan)
		}
	}()

	send := func(s string) {
		if statusChan != nil {
			select {
			case statusChan <- s:
			default:
			}
		}
	}

	// 1. Setup Context that cancels on Ctrl+C
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 2. Discover public IP for Contact header
	publicIP, err := discoverPublicIP(ctx)
	if err != nil {
		send(statusError)
		panic(fmt.Sprintf("discover public IP: %v", err))
	}
	fmt.Printf("🌐 Public IP discovered: %s (used in SIP Contact)\n", publicIP)

	// 3. Create User Agent
	ua, err := sipgo.NewUA(sipgo.WithUserAgentHostname(cfg.SipDomain))
	if err != nil {
		send(statusError)
		panic(err)
	}
	defer ua.Close()

	// 4. Create Client (Hole Punching Mode - Random Port)
	client, err := sipgo.NewClient(ua)
	if err != nil {
		send(statusError)
		panic(err)
	}

	// 5. Construct Request
	destURI := sip.Uri{User: cfg.Destination, Host: cfg.SipDomain}
	req := sip.NewRequest(sip.INVITE, destURI)

	fromVal := fmt.Sprintf("<sip:%s@%s>;tag=%d", cfg.SipUser, cfg.SipDomain, time.Now().Unix())
	req.RemoveHeader("From")
	req.AppendHeader(sip.NewHeader("From", fromVal))

	toVal := fmt.Sprintf("<sip:%s@%s>", cfg.Destination, cfg.SipDomain)
	req.RemoveHeader("To")
	req.AppendHeader(sip.NewHeader("To", toVal))

	req.RemoveHeader("Contact")
	contactHdr := sip.NewHeader("Contact", fmt.Sprintf("<sip:%s@%s>", cfg.SipUser, publicIP))
	req.AppendHeader(contactHdr)

	if cfg.OutgoingNumber != "" {
		req.AppendHeader(sip.NewHeader("P-Asserted-Identity", cfg.OutgoingNumber))
	}

	send(statusSendingInvite)

	// --- SAFETY NET: Always Hangup on Exit ---
	go func() {
		<-ctx.Done()
		fmt.Println("\n⚠️  INTERRUPT! Sending forced Hangup/Cancel...")

		cancelReq := sip.NewRequest(sip.CANCEL, destURI)
		cancelReq.RemoveHeader("From")
		cancelReq.AppendHeader(req.From())
		cancelReq.RemoveHeader("To")
		cancelReq.AppendHeader(req.To())
		cancelReq.RemoveHeader("Call-ID")
		cancelReq.AppendHeader(req.CallID())
		cancelReq.RemoveHeader("CSeq")
		cancelReq.AppendHeader(sip.NewHeader("CSeq", fmt.Sprintf("%d CANCEL", req.CSeq().SeqNo)))
		cancelReq.RemoveHeader("Via")
		cancelReq.AppendHeader(req.Via())

		client.WriteRequest(cancelReq)

		bye := sip.NewRequest(sip.BYE, destURI)
		bye.RemoveHeader("From")
		bye.AppendHeader(req.From())
		bye.RemoveHeader("To")
		bye.AppendHeader(req.To())
		bye.RemoveHeader("Call-ID")
		bye.AppendHeader(req.CallID())
		bye.RemoveHeader("CSeq")
		bye.AppendHeader(sip.NewHeader("CSeq", fmt.Sprintf("%d BYE", req.CSeq().SeqNo+1)))
		client.WriteRequest(bye)

		time.Sleep(500 * time.Millisecond)
		fmt.Println("🛑 Cleanup sent.")
	}()

	fmt.Println("----------------------------------------")
	fmt.Printf("📞 Dialing %s@%s...\n", cfg.Destination, cfg.SipDomain)
	fmt.Println("----------------------------------------")

	tx, err := client.TransactionRequest(ctx, req)
	if err != nil {
		send(statusError)
		panic(err)
	}
	defer tx.Terminate()

	// Require 100 Trying within 2s; start 12s call deadline from 100.
	const wait100 = 2 * time.Second
	const callDuration = 12 * time.Second
	const maxAuthAttempts = 3
	deadline100 := time.Now().Add(wait100)
	var callDeadline time.Time
	var deadlineTimer *time.Timer
	var authChallengeCount int

	for {
		// If we have a 12s deadline running, it takes precedence over waiting for 100.
		if !callDeadline.IsZero() {
			if deadlineTimer == nil {
				deadlineTimer = time.NewTimer(time.Until(callDeadline))
				defer deadlineTimer.Stop()
			}
			select {
			case <-ctx.Done():
				return
			case <-deadlineTimer.C:
				fmt.Println("⏱️  12s from 100 Trying — sending BYE.")
				send(statusHangingUpTimer)
				sendBYE(client, destURI, req)
				return
			case res, ok := <-tx.Responses():
				if !ok {
					return
				}
				fmt.Printf("⬅️  Received: %d %s\n", res.StatusCode, res.Reason)
				handled, done := handleResponseAfter100(client, destURI, req, res, callDeadline, send)
				if done {
					return
				}
				if handled {
					continue
				}
				// 401/407: resend INVITE with digest auth, but give up after max attempts
				if res.StatusCode == 401 || res.StatusCode == 407 {
					authChallengeCount++
					fmt.Printf("🔐 Auth challenge %d/%d (407/401)\n", authChallengeCount, maxAuthAttempts)
					if authChallengeCount > maxAuthAttempts {
						fmt.Printf("❌ Too many auth challenges (%d) — giving up.\n", authChallengeCount)
						send(statusError)
						return
					}
					send(statusAuthenticating)
					newTx, authErr := client.TransactionDigestAuth(ctx, req, res, sipgo.DigestAuth{
						Username: cfg.SipUser, Password: cfg.SipPass,
					})
					if authErr != nil {
						fmt.Printf("❌ Auth apply error: %v\n", authErr)
						send(statusError)
						return
					}
					tx.Terminate()
					tx = newTx
					continue
				}
				continue
			case <-tx.Done():
				return
			}
		}

		// Phase 1: wait for 100 Trying within 2s
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(deadline100)):
			fmt.Println("❌ No 100 Trying within 2s — cancelling.")
			send(statusError)
			sendCANCEL(client, destURI, req)
			return
		case res, ok := <-tx.Responses():
			if !ok {
				return
			}
			fmt.Printf("⬅️  Received: %d %s\n", res.StatusCode, res.Reason)
			if res.StatusCode == 100 {
				send(statusTrying)
				callDeadline = time.Now().Add(callDuration)
				fmt.Printf("⏱️  100 Trying — 12s call timer started (BYE at %s).\n", callDeadline.Format("15:04:05"))
				continue
			}
			if res.StatusCode == 401 || res.StatusCode == 407 {
				authChallengeCount++
				fmt.Printf("🔐 Auth challenge %d/%d (407/401, no 100 yet)\n", authChallengeCount, maxAuthAttempts)
				if authChallengeCount > maxAuthAttempts {
					fmt.Printf("❌ Too many auth challenges (%d) — giving up.\n", authChallengeCount)
					send(statusError)
					return
				}
				send(statusAuthenticating)
				newTx, authErr := client.TransactionDigestAuth(ctx, req, res, sipgo.DigestAuth{
					Username: cfg.SipUser, Password: cfg.SipPass,
				})
				if authErr != nil {
					fmt.Printf("❌ Auth apply error: %v\n", authErr)
					send(statusError)
					return
				}
				tx.Terminate()
				tx = newTx
				deadline100 = time.Now().Add(wait100) // require 100 within 2s for this INVITE too
				continue
			}
			if res.StatusCode == 200 {
				callDeadline = time.Now().Add(callDuration)
				handleCallEstablished(client, destURI, req, callDeadline, send)
				return
			}
			if res.StatusCode >= 300 {
				fmt.Printf("❌ Call Failed: %s\n", res.Reason)
				send(statusError)
				return
			}
		case <-tx.Done():
			return
		}
	}
}

// handleResponseAfter100 handles 100/200/4xx after we already got 100. Returns (handled, done).
func handleResponseAfter100(client *sipgo.Client, destURI sip.Uri, req *sip.Request, res *sip.Response, callDeadline time.Time, send func(string)) (handled, done bool) {
	if res.StatusCode == 100 {
		return true, false
	}
	if res.StatusCode == 200 {
		handleCallEstablished(client, destURI, req, callDeadline, send)
		return true, true
	}
	if res.StatusCode >= 300 {
		fmt.Printf("❌ Call Failed: %s\n", res.Reason)
		if send != nil {
			send(statusError)
		}
		return true, true
	}
	return false, false
}

func sendCANCEL(client *sipgo.Client, destURI sip.Uri, req *sip.Request) {
	cancelReq := sip.NewRequest(sip.CANCEL, destURI)
	cancelReq.RemoveHeader("From")
	cancelReq.AppendHeader(req.From())
	cancelReq.RemoveHeader("To")
	cancelReq.AppendHeader(req.To())
	cancelReq.RemoveHeader("Call-ID")
	cancelReq.AppendHeader(req.CallID())
	cancelReq.RemoveHeader("CSeq")
	cancelReq.AppendHeader(sip.NewHeader("CSeq", fmt.Sprintf("%d CANCEL", req.CSeq().SeqNo)))
	cancelReq.RemoveHeader("Via")
	cancelReq.AppendHeader(req.Via())
	client.WriteRequest(cancelReq)
	fmt.Println("🛑 CANCEL sent.")
}

func sendBYE(client *sipgo.Client, destURI sip.Uri, req *sip.Request) {
	bye := sip.NewRequest(sip.BYE, destURI)
	bye.RemoveHeader("From")
	bye.AppendHeader(req.From())
	bye.RemoveHeader("To")
	bye.AppendHeader(req.To())
	bye.RemoveHeader("Call-ID")
	bye.AppendHeader(req.CallID())
	bye.RemoveHeader("CSeq")
	bye.AppendHeader(sip.NewHeader("CSeq", fmt.Sprintf("%d BYE", req.CSeq().SeqNo+1)))
	bye.RemoveHeader("Via")
	bye.AppendHeader(req.Via())
	client.WriteRequest(bye)
	fmt.Println("🛑 BYE sent.")
}

func handleCallEstablished(client *sipgo.Client, destURI sip.Uri, req *sip.Request, callDeadline time.Time, send func(string)) {
	fmt.Println("✅ CALL ESTABLISHED! (200 OK) — sending ACK.")
	ack := sip.NewRequest(sip.ACK, destURI)
	client.WriteRequest(ack)
	if until := time.Until(callDeadline); until > 0 {
		fmt.Printf("⏱️  Sending BYE in %v (12s from 100).\n", until.Round(time.Millisecond))
		time.Sleep(until)
	}
	if send != nil {
		send(statusHangingUpTimer)
	}
	sendBYE(client, destURI, req)
}
