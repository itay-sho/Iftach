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
	CallToken      string `kong:"help='Token required for WebSocket /call'"`
	ListenAddress  string `kong:"help='HTTP server listen address'"`
	ListenPort     int    `kong:"help='HTTP server listen port'"`
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
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no, viewport-fit=cover">
    <title>Gate Control</title>
    <style>
        :root {
            --bg-color: #000000;
            --main-green: #00ff41; /* Hacker/Neon Green */
            --main-grey: #666666;
            --main-red: #ff3333;
            --font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
        }

        body {
            background-color: var(--bg-color);
            color: white;
            font-family: var(--font-family);
            margin: 0;
            /* Use dvh (Dynamic Viewport Height) to account for mobile address bars */
            height: 100vh;
            height: 100dvh; 
            display: flex;
            flex-direction: column;
            align-items: center;
            justify-content: space-between; 
            overflow: hidden; 
        }

        /* --- Main Layout --- */
        .container {
            flex-grow: 1;
            display: flex;
            flex-direction: column;
            justify-content: center;
            align-items: center;
            width: 100%;
        }

        /* --- The Big Button --- */
        #open-btn {
            width: 250px;
            height: 250px;
            border-radius: 50%;
            background: transparent;
            font-size: 2rem;
            font-weight: 700;
            text-transform: uppercase;
            cursor: pointer;
            border: 4px solid currentColor;
            transition: all 0.3s ease;
            outline: none;
            -webkit-tap-highlight-color: transparent;
            display: flex;
            align-items: center;
            justify-content: center;
            user-select: none;
        }

        #open-btn:active {
            transform: scale(0.95);
        }

        /* Button States */
        .state-ready {
            color: var(--main-green);
            box-shadow: 0 0 20px rgba(0, 255, 65, 0.2);
        }

        .state-disabled {
            color: var(--main-grey);
            border-color: var(--main-grey);
            pointer-events: none;
            box-shadow: none;
        }

        .state-error {
            color: var(--main-red);
            box-shadow: 0 0 20px rgba(255, 51, 51, 0.3);
            animation: shake 0.5s;
        }

        @keyframes shake {
            0% { transform: translate(1px, 1px) rotate(0deg); }
            10% { transform: translate(-1px, -2px) rotate(-1deg); }
            20% { transform: translate(-3px, 0px) rotate(1deg); }
            30% { transform: translate(3px, 2px) rotate(0deg); }
            40% { transform: translate(1px, -1px) rotate(1deg); }
            50% { transform: translate(-1px, 2px) rotate(-1deg); }
            60% { transform: translate(-3px, 1px) rotate(0deg); }
            70% { transform: translate(3px, 1px) rotate(-1deg); }
            80% { transform: translate(-1px, -1px) rotate(1deg); }
            90% { transform: translate(1px, 2px) rotate(0deg); }
            100% { transform: translate(1px, -2px) rotate(-1deg); }
        }

        /* --- Status Log --- */
        #status-display {
            margin-top: 40px;
            height: 30px;
            color: #aaa;
            font-family: monospace;
            font-size: 1rem;
            text-align: center;
            padding: 0 20px;
        }

        /* --- Footer / Settings --- */
        .footer {
            width: 100%;
            display: flex;
            justify-content: center;
            /* Extra padding for mobile bottom bar / safe area */
            padding-bottom: max(30px, env(safe-area-inset-bottom));
            padding-top: 20px;
            background: linear-gradient(to top, black 20%, transparent); /* slight fade to ensure readability */
        }

        #settings-trigger {
            background: transparent;
            border: 1px solid #333;
            color: #888;
            padding: 12px 24px; /* Larger touch target */
            border-radius: 30px;
            font-size: 1rem;
            cursor: pointer;
            transition: color 0.2s;
            -webkit-tap-highlight-color: transparent;
        }
        
        #settings-trigger.has-token {
            color: var(--main-green);
            border-color: var(--main-green);
        }

        /* --- Modal --- */
        .modal-overlay {
            position: fixed;
            top: 0; left: 0; right: 0; bottom: 0;
            background: rgba(0,0,0,0.95);
            display: flex;
            justify-content: center;
            align-items: center;
            opacity: 0;
            pointer-events: none;
            transition: opacity 0.3s ease;
            z-index: 100;
            backdrop-filter: blur(5px);
        }

        .modal-overlay.active {
            opacity: 1;
            pointer-events: auto;
        }

        .modal-content {
            width: 85%;
            max-width: 350px;
            display: flex;
            flex-direction: column;
            gap: 15px;
        }

        input[type="text"] {
            background: #111;
            border: 2px solid var(--main-green);
            color: white;
            padding: 15px;
            font-size: 1.1rem;
            text-align: center;
            border-radius: 8px;
            outline: none;
            width: 100%;
            box-sizing: border-box; /* Fixes padding issues */
        }

        .btn-action {
            background: transparent;
            border: 2px solid var(--main-green);
            color: var(--main-green);
            padding: 15px;
            font-size: 1rem;
            font-weight: bold;
            cursor: pointer;
            border-radius: 8px;
            text-transform: uppercase;
            width: 100%;
        }

        .btn-action.secondary {
            border-color: var(--main-grey);
            color: var(--main-grey);
        }
        
        .btn-action.danger {
            border-color: var(--main-red);
            color: var(--main-red);
        }
    </style>
</head>
<body>

    <div class="container">
        <button id="open-btn" class="state-ready">OPEN</button>
        <div id="status-display">Ready</div>
    </div>

    <div class="footer">
        <button id="settings-trigger">Set Token</button>
    </div>

    <div id="modal" class="modal-overlay">
        <div class="modal-content">
            <h2 style="text-align: center; color: var(--main-green); margin: 0 0 10px 0;">Setup</h2>
            
            <input type="text" id="token-input" placeholder="Paste Token Here" autocomplete="off">

            <button id="save-token" class="btn-action">Save Token</button>
            <button id="clear-token" class="btn-action danger">Clear Token</button>
            <button id="close-modal" class="btn-action secondary">Cancel</button>
        </div>
    </div>

    <script>
        // --- Constants & State ---
        const TOKEN_KEY = 'token';
        const STATUS_LABELS = {
            sending_invite: 'Sending INVITE...',
            authenticating: 'Authenticating...',
            trying: 'Trying (100)...',
            hanging_up_timer: 'Hanging up (12s timer)',
            error: 'Error — check logs'
        };

        const els = {
            btn: document.getElementById('open-btn'),
            status: document.getElementById('status-display'),
            settingsTrigger: document.getElementById('settings-trigger'),
            modal: document.getElementById('modal'),
            input: document.getElementById('token-input'),
            saveBtn: document.getElementById('save-token'),
            clearBtn: document.getElementById('clear-token'),
            closeBtn: document.getElementById('close-modal')
        };

        // --- Core Functions ---

        function getToken() { 
            return localStorage.getItem(TOKEN_KEY) || ''; 
        }

        function setToken(v) { 
            if(v) {
                localStorage.setItem(TOKEN_KEY, v); 
            } else {
                localStorage.removeItem(TOKEN_KEY);
            }
            updateSettingsUI();
        }

        function updateSettingsUI() {
            const token = getToken();
            els.input.value = token;
            
            if (token) {
                els.settingsTrigger.textContent = "Token Set (Change)";
                els.settingsTrigger.classList.add('has-token');
            } else {
                els.settingsTrigger.textContent = "Token Unset (Set)";
                els.settingsTrigger.classList.remove('has-token');
            }
        }

        function setStatus(text) {
            els.status.textContent = text;
        }

        function setButtonState(state) {
            els.btn.className = '';
            els.btn.disabled = false;

            if (state === 'ready') {
                els.btn.classList.add('state-ready');
                els.btn.textContent = 'OPEN';
            } else if (state === 'processing') {
                els.btn.classList.add('state-disabled');
                els.btn.disabled = true;
                els.btn.textContent = '...';
            } else if (state === 'error') {
                els.btn.classList.add('state-error');
                els.btn.textContent = 'FAILED';
                setTimeout(() => setButtonState('ready'), 2000);
            }
        }

        // --- WebSocket Logic ---

        function triggerOpen() {
            setStatus('');
            setButtonState('processing');

            const token = getToken();
            let wsUrl = (location.protocol === 'https:' ? 'wss:' : 'ws:') + '//' + location.host + '/call';
            if (token) wsUrl += '?token=' + encodeURIComponent(token);

            const ws = new WebSocket(wsUrl);
            let hasError = false;

            ws.onopen = function() {
                setStatus('Connected — call started');
            };

            ws.onmessage = function(ev) {
                try {
                    const msg = JSON.parse(ev.data);
                    const label = STATUS_LABELS[msg.status] || msg.status;
                    setStatus(label);
                    if (msg.status === 'error') { 
                        hasError = true;
                        ws.close(); 
                    }
                } catch (e) {
                    setStatus('Invalid message received');
                }
            };

            ws.onerror = function() {
                setStatus('WebSocket connection error');
                hasError = true;
            };

            ws.onclose = function(ev) {
                if (ev.code === 4001) {
                    setStatus('4001: Wrong credentials');
                    hasError = true;
                } else if (!hasError) {
                    setStatus('Connection closed');
                }

                if (hasError) {
                    setButtonState('error');
                } else {
                    setButtonState('ready');
                }
            };
        }

        // --- Event Listeners ---

        (function() {
            const params = new URLSearchParams(location.search);
            const q = params.get('token');
            if (q !== null) {
                setToken(q);
                history.replaceState({}, '', location.pathname);
            }
            updateSettingsUI();
        })();

        els.btn.onclick = triggerOpen;

        els.settingsTrigger.onclick = () => {
            els.modal.classList.add('active');
            // Small delay to allow modal to render before focusing (fixes some mobile keyboard glitches)
            setTimeout(() => els.input.focus(), 100);
        };

        const closeModal = () => {
            els.modal.classList.remove('active');
            els.input.blur(); // Hide keyboard
        }
        
        els.closeBtn.onclick = closeModal;
        els.modal.onclick = (e) => {
            if (e.target === els.modal) closeModal();
        };

        els.saveBtn.onclick = () => {
            setToken(els.input.value.trim());
            closeModal();
            setStatus('Token saved');
        };

        els.clearBtn.onclick = () => {
            setToken('');
            els.input.value = '';
            closeModal();
            setStatus('Token cleared');
        };

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
		if tokenFromRequest(r) != cli.CallToken {
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

	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", cli.ListenAddress, cli.ListenPort), Handler: r}
	go func() {
		fmt.Printf("🌐 HTTP server listening on %s:%d (WebSocket /call to start a call)\n", cli.ListenAddress, cli.ListenPort)
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
