package mail

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeSMTP accepts one session on localhost and hands back what DATA carried.
func fakeSMTP(t *testing.T) (string, chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		fmt.Fprint(c, "220 fake\r\n")
		var data, rcpt strings.Builder
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch cmd := strings.ToUpper(strings.Fields(l)[0]); cmd {
			case "EHLO":
				fmt.Fprint(c, "250-fake\r\n250 AUTH PLAIN\r\n")
			case "AUTH":
				fmt.Fprint(c, "235 ok\r\n")
			case "RCPT":
				rcpt.WriteString(l)
				fmt.Fprint(c, "250 ok\r\n")
			case "DATA":
				fmt.Fprint(c, "354 go\r\n")
				for {
					l, _ := r.ReadString('\n')
					if l == ".\r\n" {
						break
					}
					data.WriteString(l)
				}
				fmt.Fprint(c, "250 queued\r\n")
			case "QUIT":
				fmt.Fprint(c, "221 bye\r\n")
				got <- rcpt.String() + data.String()
				return
			default:
				fmt.Fprint(c, "250 ok\r\n")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestSendWithInvite(t *testing.T) {
	addr, got := fakeSMTP(t)
	a := Account{Address: "po@pep.ir", Password: "app", Name: "Sara PO", SMTP: addr}
	start := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	m := Message{To: Split("dev@pep.ir, QA <qa@pep.ir>"), Bcc: []string{"pm@pep.ir"}, Subject: "Sprint review", Body: "agenda", Invite: &Invite{Start: start}}
	if err := Send(a, m); err != nil {
		t.Fatal(err)
	}
	s := <-got
	for _, want := range []string{"RCPT TO:<qa@pep.ir>", "RCPT TO:<pm@pep.ir>", "Subject: Sprint review", "method=REQUEST",
		"DTSTART:20261027T100000Z", "DTEND:20261027T103000Z", "ATTENDEE;ROLE=REQ-PARTICIPANT;RSVP=TRUE:mailto:qa@pep.ir"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "Bcc") || strings.Contains(s, "mailto:pm@pep.ir") {
		t.Error("bcc leaked into the message")
	}
}

func TestPlainRefusedOffLocalhost(t *testing.T) {
	if !local("127.0.0.1:25") || !local("localhost:1") || local("smtp.gmail.com:587") {
		t.Error("local")
	}
}

// fakeIMAP serves one mailbox of two messages.
func fakeIMAP(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	msgs := map[string]string{
		"7": "From: Ali <ali@pep.ir>\r\nSubject: =?utf-8?q?Report_=E2=9C=93?=\r\nDate: Mon, 26 Oct 2026 09:00:00 +0000\r\n\r\n",
		"9": "From: Bob <bob@x.io>\r\nSubject: Hello\r\nDate: Tue, 27 Oct 2026 09:00:00 +0000\r\nContent-Type: multipart/alternative; boundary=b\r\n\r\n--b\r\nContent-Type: text/html\r\n\r\n<p>hi html</p>\r\n--b\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\naGkgdGV4\r\ndA==\r\n--b--\r\n",
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				fmt.Fprint(c, "* OK fake\r\n")
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					f := strings.Fields(l)
					tag, cmd := f[0], strings.Join(f[1:], " ")
					switch {
					case strings.HasPrefix(cmd, "LOGIN"):
						if !strings.Contains(cmd, `"secret"`) {
							fmt.Fprintf(c, "%s NO bad login\r\n", tag)
							continue
						}
					case strings.HasPrefix(cmd, "UID SEARCH"):
						fmt.Fprint(c, "* SEARCH 7 9\r\n")
					case strings.HasPrefix(cmd, "UID FETCH 7,9"):
						for _, u := range []string{"7", "9"} {
							h := msgs[u][:strings.Index(msgs[u], "\r\n\r\n")+4]
							fmt.Fprintf(c, "* 1 FETCH (UID %s FLAGS (\\Seen) BODY[HEADER.FIELDS (FROM SUBJECT DATE)] {%d}\r\n%s)\r\n", u, len(h), h)
						}
					case strings.HasPrefix(cmd, "UID FETCH 9"):
						fmt.Fprintf(c, "* 2 FETCH (UID 9 BODY[] {%d}\r\n%s)\r\n", len(msgs["9"]), msgs["9"])
					}
					fmt.Fprintf(c, "%s OK done\r\n", tag)
				}
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func TestInboxAndRead(t *testing.T) {
	a := Account{Address: "po@pep.ir", Password: "secret", IMAP: fakeIMAP(t)}
	ms, err := Inbox(a, "INBOX", "", 50)
	if err != nil || len(ms) != 2 {
		t.Fatalf("%v %v", ms, err)
	}
	if ms[0].UID != "9" || ms[1].Subject != "Report ✓" || !ms[1].Seen {
		t.Errorf("%+v", ms)
	}
	m, err := Read(a, "INBOX", "9")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(Text(m)); got != "hi text" {
		t.Errorf("text %q", got)
	}
	a.Password = "wrong"
	if _, err := Inbox(a, "INBOX", "", 5); err == nil || !strings.Contains(err.Error(), "bad login") {
		t.Errorf("login: %v", err)
	}
}
