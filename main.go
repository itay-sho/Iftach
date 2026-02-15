package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
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
	PublicIP    string `kong:"required,help='Your static public IP'"`
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

func run(cfg *Config) {
	// 1. Setup Context that cancels on Ctrl+C
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 2. Create User Agent
	ua, err := sipgo.NewUA(sipgo.WithUserAgentHostname(cfg.SipDomain))
	if err != nil {
		panic(err)
	}
	defer ua.Close()

	// 3. Create Client (Hole Punching Mode - Random Port)
	client, err := sipgo.NewClient(ua)
	if err != nil {
		panic(err)
	}

	// 4. Construct Request
	destURI := sip.Uri{User: cfg.Destination, Host: cfg.SipDomain}
	req := sip.NewRequest(sip.INVITE, destURI)

	fromVal := fmt.Sprintf("<sip:%s@%s>;tag=%d", cfg.SipUser, cfg.SipDomain, time.Now().Unix())
	req.RemoveHeader("From")
	req.AppendHeader(sip.NewHeader("From", fromVal))

	toVal := fmt.Sprintf("<sip:%s@%s>", cfg.Destination, cfg.SipDomain)
	req.RemoveHeader("To")
	req.AppendHeader(sip.NewHeader("To", toVal))

	req.RemoveHeader("Contact")
	contactHdr := sip.NewHeader("Contact", fmt.Sprintf("<sip:%s@%s>", cfg.SipUser, cfg.PublicIP))
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
