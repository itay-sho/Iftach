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
)

// Config holds SIP and call parameters (from CLI, env, or config files).
type Config struct {
	SipUser     string `kong:"required,help='SIP user (Zadarma ID)'"`
	SipPass     string `kong:"required,help='SIP password'"`
	SipDomain   string `kong:"required,help='SIP domain'"`
	Destination string `kong:"required,help='Number to call'"`
}

var cli Config

func main() {
	kong.Parse(&cli,
		kong.Name("Iftach"),
		kong.Description("SIP client to place a call"),
		kong.DefaultEnvars("IFTACH"), // read SIP_USER, SIP_PASS, etc. from env (e.g. .env via launch.json envFile)
	)
	run(&cli)
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

func run(cfg *Config) {
	// 1. Setup Context that cancels on Ctrl+C
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 2. Discover public IP for Contact header
	publicIP, err := discoverPublicIP(ctx)
	if err != nil {
		panic(fmt.Sprintf("discover public IP: %v", err))
	}
	fmt.Printf("🌐 Public IP discovered: %s (used in SIP Contact)\n", publicIP)

	// 3. Create User Agent
	ua, err := sipgo.NewUA(sipgo.WithUserAgentHostname(cfg.SipDomain))
	if err != nil {
		panic(err)
	}
	defer ua.Close()

	// 4. Create Client (Hole Punching Mode - Random Port)
	client, err := sipgo.NewClient(ua)
	if err != nil {
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
		fmt.Println("🛑 Cleanup sent. Exiting.")
		os.Exit(0)
	}()

	fmt.Println("----------------------------------------")
	fmt.Printf("📞 Dialing %s@%s...\n", cfg.Destination, cfg.SipDomain)
	fmt.Println("----------------------------------------")

	tx, err := client.TransactionRequest(ctx, req)
	if err != nil {
		panic(err)
	}
	defer tx.Terminate()

	for {
		select {
		case <-ctx.Done():
			return
		case res := <-tx.Responses():
			fmt.Printf("⬅️  Received: %d %s\n", res.StatusCode, res.Reason)

			if res.StatusCode == 401 || res.StatusCode == 407 {
				fmt.Println("🔐 Authenticating...")

				authRes, err := client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{
					Username: cfg.SipUser, Password: cfg.SipPass,
				})
				if err != nil {
					fmt.Printf("❌ Auth Error: %v\n", err)
					return
				}

				fmt.Printf("⬅️  Auth Result: %d %s\n", authRes.StatusCode, authRes.Reason)

				if authRes.StatusCode == 200 {
					handleCallEstablished(client, destURI)
					return
				}
				if authRes.StatusCode >= 300 {
					fmt.Println("❌ Failed after auth.")
					return
				}
				continue
			}

			if res.StatusCode == 200 {
				handleCallEstablished(client, destURI)
				return
			}

			if res.StatusCode >= 300 {
				fmt.Printf("❌ Call Failed: %s\n", res.Reason)
				return
			}
		case <-tx.Done():
			return
		}
	}
}

func handleCallEstablished(client *sipgo.Client, destURI sip.Uri) {
	fmt.Println("✅ CALL ESTABLISHED! (200 OK)")

	// Send ACK
	ack := sip.NewRequest(sip.ACK, destURI)
	client.WriteRequest(ack)

	fmt.Println("ℹ️  Press Ctrl+C to hangup.")
	// Just wait forever until Ctrl+C (handled by the Safety Net goroutine)
	select {}
}
