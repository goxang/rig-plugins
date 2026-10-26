// Package mail is email as notify/1 and more: send over SMTP (with a calendar invite), and read an
// inbox over IMAP. Gmail and Outlook need an app password; the account lives in the plugin's secrets.
package mail

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig/sdk"
)

// Presets are the servers of the providers a connect form offers.
var Presets = map[string][2]string{
	"gmail":   {"smtp.gmail.com:587", "imap.gmail.com:993"},
	"outlook": {"smtp.office365.com:587", "outlook.office365.com:993"},
	"yahoo":   {"smtp.mail.yahoo.com:465", "imap.mail.yahoo.com:993"},
}

// Account is a mailbox: its address, app password, and SMTP and IMAP servers as host:port.
type Account struct {
	Address, Password, Name, SMTP, IMAP string
}

func load(c *sdk.Ctx) (Account, error) {
	a := Account{Address: forge.Setting(c, "MAIL", "address"), Password: forge.Setting(c, "MAIL", "password"),
		Name: forge.Setting(c, "MAIL", "name"), SMTP: forge.Setting(c, "MAIL", "smtp"), IMAP: forge.Setting(c, "MAIL", "imap")}
	if a.Address == "" || a.SMTP == "" {
		return a, sdk.Unavailable("mail is not connected", "connect a mailbox on the Mail tab")
	}
	return a, nil
}

// local is a server on this machine, the only place plain text is allowed (tests, a dev relay).
func local(addr string) bool {
	h, _, _ := net.SplitHostPort(addr)
	ip := net.ParseIP(h)
	return h == "localhost" || ip != nil && ip.IsLoopback()
}

// Message is one email; Invite, when set, rides along as a calendar request.
type Message struct {
	To, Cc, Bcc   []string
	Subject, Body string
	InReplyTo     string
	Invite        *Invite
}

// Invite is a meeting an email asks its recipients to.
type Invite struct {
	Start, End time.Time
	Location   string
}

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Build renders m from a as RFC 5322 bytes.
func Build(a Account, m Message, now time.Time) []byte {
	var b bytes.Buffer
	from := (&mail.Address{Name: a.Name, Address: a.Address}).String()
	hdr := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	hdr("From", from)
	hdr("To", strings.Join(m.To, ", "))
	if len(m.Cc) > 0 {
		hdr("Cc", strings.Join(m.Cc, ", "))
	}
	hdr("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	hdr("Date", now.Format(time.RFC1123Z))
	_, domain, _ := strings.Cut(a.Address, "@")
	hdr("Message-ID", "<"+newID()+"@"+domain+">")
	if m.InReplyTo != "" {
		hdr("In-Reply-To", m.InReplyTo)
		hdr("References", m.InReplyTo)
	}
	hdr("MIME-Version", "1.0")
	body := strings.ReplaceAll(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n", "\r\n")
	if m.Invite == nil {
		hdr("Content-Type", `text/plain; charset="utf-8"`)
		b.WriteString("\r\n" + body + "\r\n")
		return b.Bytes()
	}
	mw := multipart.NewWriter(&b)
	hdr("Content-Type", `multipart/mixed; boundary="`+mw.Boundary()+`"`)
	b.WriteString("\r\n")
	w, _ := mw.CreatePart(map[string][]string{"Content-Type": {`text/plain; charset="utf-8"`}})
	io.WriteString(w, body+"\r\n")
	w, _ = mw.CreatePart(map[string][]string{"Content-Type": {`text/calendar; charset="utf-8"; method=REQUEST`}, "Content-Disposition": {`attachment; filename="invite.ics"`}})
	io.WriteString(w, ICS(a, m, now))
	mw.Close()
	return b.Bytes()
}

// ICS is m's invite as an iCalendar request.
func ICS(a Account, m Message, now time.Time) string {
	esc := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\n", `\n`).Replace
	ts := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	end := m.Invite.End
	if !end.After(m.Invite.Start) {
		end = m.Invite.Start.Add(30 * time.Minute)
	}
	l := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//rig//mail//EN", "METHOD:REQUEST", "BEGIN:VEVENT",
		"UID:" + newID() + "@rig", "DTSTAMP:" + ts(now), "DTSTART:" + ts(m.Invite.Start), "DTEND:" + ts(end),
		"SUMMARY:" + esc(m.Subject), "DESCRIPTION:" + esc(m.Body), "ORGANIZER;CN=" + esc(a.Name) + ":mailto:" + a.Address}
	if m.Invite.Location != "" {
		l = append(l, "LOCATION:"+esc(m.Invite.Location))
	}
	for _, t := range append(append([]string{}, m.To...), m.Cc...) {
		if ad, err := mail.ParseAddress(t); err == nil {
			l = append(l, "ATTENDEE;ROLE=REQ-PARTICIPANT;RSVP=TRUE:mailto:"+ad.Address)
		}
	}
	return strings.Join(append(l, "END:VEVENT", "END:VCALENDAR"), "\r\n") + "\r\n"
}

// Send delivers m through a's SMTP server: implicit TLS on 465, else STARTTLS (plain only to a local server).
func Send(a Account, m Message) error {
	host, port, err := net.SplitHostPort(a.SMTP)
	if err != nil {
		return fmt.Errorf("smtp server %q: want host:port", a.SMTP)
	}
	var conn net.Conn
	d := &net.Dialer{Timeout: 15 * time.Second}
	if port == "465" {
		conn, err = tls.DialWithDialer(d, "tcp", a.SMTP, &tls.Config{ServerName: host})
	} else {
		conn, err = d.Dial("tcp", a.SMTP)
	}
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(time.Minute))
	cl, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer cl.Close()
	if ok, _ := cl.Extension("STARTTLS"); ok && port != "465" {
		if err := cl.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	} else if port != "465" && !local(a.SMTP) {
		return fmt.Errorf("%s offers no TLS; refusing to send the password in clear", a.SMTP)
	}
	if a.Password != "" {
		if err := cl.Auth(smtp.PlainAuth("", a.Address, a.Password, host)); err != nil {
			return err
		}
	}
	if m.To == nil && m.Cc == nil && m.Bcc == nil {
		return cl.Quit()
	}
	if err := cl.Mail(a.Address); err != nil {
		return err
	}
	for _, r := range append(append(append([]string{}, m.To...), m.Cc...), m.Bcc...) {
		ad, err := mail.ParseAddress(r)
		if err != nil {
			return fmt.Errorf("recipient %q: %w", r, err)
		}
		if err := cl.Rcpt(ad.Address); err != nil {
			return fmt.Errorf("%s: %w", r, err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(Build(a, m, time.Now())); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

// Split reads "a@x, Bea <b@y>; c@z" as addresses.
func Split(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------- IMAP: just enough to list a folder and read a message ----------

type imapConn struct {
	c   net.Conn
	r   *bufio.Reader
	tag int
}

func dialIMAP(a Account) (*imapConn, error) {
	if a.IMAP == "" {
		return nil, fmt.Errorf("no IMAP server: connect the mailbox again with one")
	}
	host, _, err := net.SplitHostPort(a.IMAP)
	if err != nil {
		return nil, fmt.Errorf("imap server %q: want host:port", a.IMAP)
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	var c net.Conn
	if local(a.IMAP) {
		c, err = d.Dial("tcp", a.IMAP)
	} else {
		c, err = tls.DialWithDialer(d, "tcp", a.IMAP, &tls.Config{ServerName: host})
	}
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(time.Minute))
	ic := &imapConn{c: c, r: bufio.NewReader(c)}
	if _, err := ic.r.ReadString('\n'); err != nil {
		c.Close()
		return nil, err
	}
	if _, err := ic.cmd("LOGIN " + quote(a.Address) + " " + quote(a.Password)); err != nil {
		c.Close()
		return nil, err
	}
	return ic, nil
}

func quote(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }

var literal = regexp.MustCompile(`\{(\d+)\}\r\n$`)

// cmd runs one command and returns its untagged responses, each with its literals inlined.
func (ic *imapConn) cmd(line string) ([]string, error) {
	ic.tag++
	tag := "r" + strconv.Itoa(ic.tag)
	if _, err := fmt.Fprintf(ic.c, "%s %s\r\n", tag, line); err != nil {
		return nil, err
	}
	var out []string
	for {
		var resp strings.Builder
		for {
			l, err := ic.r.ReadString('\n')
			if err != nil {
				return nil, err
			}
			resp.WriteString(l)
			m := literal.FindStringSubmatch(l)
			if m == nil {
				break
			}
			n, _ := strconv.Atoi(m[1])
			buf := make([]byte, n)
			if _, err := io.ReadFull(ic.r, buf); err != nil {
				return nil, err
			}
			resp.Write(buf)
		}
		s := resp.String()
		if strings.HasPrefix(s, tag+" ") {
			if st := strings.Fields(s); len(st) > 1 && st[1] == "OK" {
				return out, nil
			}
			return nil, fmt.Errorf("imap: %s", strings.TrimSpace(strings.TrimPrefix(s, tag+" ")))
		}
		out = append(out, s)
	}
}

func (ic *imapConn) close() { ic.cmd("LOGOUT"); ic.c.Close() }

// Mail is a message in a folder list.
type Mail struct {
	UID     string    `json:"uid"`
	From    string    `json:"from"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
	Seen    bool      `json:"seen"`
}

var (
	uidRe   = regexp.MustCompile(`UID (\d+)`)
	flagsRe = regexp.MustCompile(`FLAGS \(([^)]*)\)`)
)

func decode(s string) string {
	if d, err := new(mime.WordDecoder).DecodeHeader(s); err == nil {
		return d
	}
	return s
}

// Inbox lists the newest limit messages of folder, newest first; query is an IMAP SEARCH (default ALL).
func Inbox(a Account, folder, query string, limit int) ([]Mail, error) {
	ic, err := dialIMAP(a)
	if err != nil {
		return nil, err
	}
	defer ic.close()
	if _, err := ic.cmd("EXAMINE " + quote(folder)); err != nil {
		return nil, err
	}
	if query == "" {
		query = "ALL"
	}
	res, err := ic.cmd("UID SEARCH " + query)
	if err != nil {
		return nil, err
	}
	var uids []int
	for _, l := range res {
		if f := strings.Fields(l); len(f) > 1 && f[1] == "SEARCH" {
			for _, u := range f[2:] {
				if n, err := strconv.Atoi(u); err == nil {
					uids = append(uids, n)
				}
			}
		}
	}
	sort.Ints(uids)
	if len(uids) > limit {
		uids = uids[len(uids)-limit:]
	}
	if len(uids) == 0 {
		return nil, nil
	}
	set := make([]string, len(uids))
	for i, u := range uids {
		set[i] = strconv.Itoa(u)
	}
	res, err = ic.cmd("UID FETCH " + strings.Join(set, ",") + " (UID FLAGS BODY.PEEK[HEADER.FIELDS (FROM SUBJECT DATE)])")
	if err != nil {
		return nil, err
	}
	var out []Mail
	for _, l := range res {
		m := uidRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		it := Mail{UID: m[1]}
		if f := flagsRe.FindStringSubmatch(l); f != nil {
			it.Seen = strings.Contains(f[1], `\Seen`)
		}
		if i := strings.Index(l, "}\r\n"); i >= 0 {
			if msg, err := mail.ReadMessage(strings.NewReader(l[i+3:])); err == nil {
				it.From, it.Subject = decode(msg.Header.Get("From")), decode(msg.Header.Get("Subject"))
				it.Date, _ = msg.Header.Date()
			}
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out, nil
}

// Read is one message's raw bytes.
func Read(a Account, folder, uid string) (*mail.Message, error) {
	if _, err := strconv.Atoi(uid); err != nil {
		return nil, fmt.Errorf("uid %q is not a number", uid)
	}
	ic, err := dialIMAP(a)
	if err != nil {
		return nil, err
	}
	defer ic.close()
	if _, err := ic.cmd("EXAMINE " + quote(folder)); err != nil {
		return nil, err
	}
	res, err := ic.cmd("UID FETCH " + uid + " (BODY.PEEK[])")
	if err != nil {
		return nil, err
	}
	for _, l := range res {
		if i := strings.Index(l, "}\r\n"); i >= 0 {
			return mail.ReadMessage(strings.NewReader(l[i+3:]))
		}
	}
	return nil, fmt.Errorf("no message %s in %s", uid, folder)
}

// Text is a message's plain text: the text/plain part of a multipart one.
func Text(m *mail.Message) string {
	mt, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
	return partText(mt, params, m.Header.Get("Content-Transfer-Encoding"), m.Body)
}

func partText(mt string, params map[string]string, enc string, r io.Reader) string {
	r = decodeBody(enc, r)
	if strings.HasPrefix(mt, "multipart/") {
		mr := multipart.NewReader(r, params["boundary"])
		var html string
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			pt, pp, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			t := partText(pt, pp, p.Header.Get("Content-Transfer-Encoding"), p)
			if strings.HasPrefix(pt, "text/plain") || strings.HasPrefix(pt, "multipart/") && t != "" {
				return t
			}
			if pt == "text/html" {
				html = t
			}
		}
		return html
	}
	b, _ := io.ReadAll(io.LimitReader(r, 1<<20))
	if mt == "text/html" {
		return tags.ReplaceAllString(string(b), "")
	}
	return string(b)
}

func decodeBody(enc string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &noCRLF{r})
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

// noCRLF drops the line breaks base64 bodies are wrapped with.
type noCRLF struct{ r io.Reader }

func (n *noCRLF) Read(p []byte) (int, error) {
	k, err := n.r.Read(p)
	j := 0
	for _, b := range p[:k] {
		if b != '\r' && b != '\n' {
			p[j] = b
			j++
		}
	}
	return j, err
}

var tags = regexp.MustCompile(`(?s)<style.*?</style>|<[^>]+>`)

type row struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func Register(p *sdk.Plugin) {
	p.Action("connect", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Provider string `json:"provider" title:"Provider" enum:"gmail,outlook,yahoo,other" default:"gmail"`
		Address  string `json:"address" title:"Email address" format:"email" rig:"required"`
		Password string `json:"password" title:"App password" format:"password" desc:"Gmail: myaccount.google.com/apppasswords; empty keeps the stored one"`
		Name     string `json:"name" title:"Your name"`
		SMTP     string `json:"smtp" title:"SMTP server" desc:"host:port, for other"`
		IMAP     string `json:"imap" title:"IMAP server" desc:"host:port, for other"`
	}) (*sdk.Result, error) {
		if pr, ok := Presets[in.Provider]; ok {
			in.SMTP, in.IMAP = pr[0], pr[1]
		}
		if in.SMTP == "" {
			return nil, fmt.Errorf("give the SMTP server (host:port) or pick a provider")
		}
		vals := map[string]string{"ADDRESS": in.Address, "NAME": in.Name, "SMTP": in.SMTP, "IMAP": in.IMAP}
		if in.Password != "" {
			vals["PASSWORD"] = strings.ReplaceAll(in.Password, " ", "")
		}
		for k, v := range vals {
			if err := c.SetSecret("MAIL_"+k, strings.TrimSpace(v)); err != nil {
				return nil, err
			}
		}
		a, err := load(c)
		if err != nil {
			return nil, err
		}
		if err := Send(a, Message{}); err != nil {
			return nil, fmt.Errorf("saved, but %s refused the login: %w", a.SMTP, err)
		}
		return &sdk.Result{Message: "connected " + a.Address}, nil
	}, sdk.Describe("store a mailbox (Gmail or Outlook with an app password, or any SMTP/IMAP) and test it"))

	p.Action("disconnect", sdk.EffectWrite, func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Result, error) {
		for _, k := range []string{"ADDRESS", "PASSWORD", "NAME", "SMTP", "IMAP"} {
			if err := c.SetSecret("MAIL_"+k, ""); err != nil {
				return nil, err
			}
		}
		return &sdk.Result{Message: "disconnected"}, nil
	})

	p.Source("connection", func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Frame, error) {
		a, err := load(c)
		if err != nil {
			return frames.Of([]row{{"status", "not connected: fill in the form"}}), nil
		}
		return frames.Of([]row{{"status", "connected"}, {"address", a.Address}, {"name", a.Name}, {"smtp", a.SMTP}, {"imap", a.IMAP}}), nil
	}, sdk.Cache("30s"))

	type sendIn struct {
		To       string `json:"to" rig:"required" desc:"addresses, comma separated"`
		Cc       string `json:"cc"`
		Bcc      string `json:"bcc"`
		Subject  string `json:"subject" rig:"required"`
		Body     string `json:"body" format:"textarea"`
		Start    string `json:"start" title:"Meeting start" format:"date-time" desc:"set it to send a calendar invite"`
		End      string `json:"end" title:"Meeting end" format:"date-time"`
		Location string `json:"location"`
		Reply    string `json:"reply" desc:"Message-ID this answers"`
	}
	send := func(c *sdk.Ctx, in sendIn) (*sdk.Result, error) {
		a, err := load(c)
		if err != nil {
			return nil, err
		}
		m := Message{To: Split(in.To), Cc: Split(in.Cc), Bcc: Split(in.Bcc), Subject: in.Subject, Body: in.Body, InReplyTo: in.Reply}
		if in.Start != "" {
			s, err := ParseTime(in.Start)
			if err != nil {
				return nil, err
			}
			e, _ := ParseTime(in.End)
			m.Invite = &Invite{Start: s, End: e, Location: in.Location}
		}
		if err := Send(a, m); err != nil {
			return nil, err
		}
		n := len(m.To) + len(m.Cc) + len(m.Bcc)
		return &sdk.Result{Message: fmt.Sprintf("sent %q to %d", in.Subject, n), Outputs: sdk.Args{"recipients": n}}, nil
	}
	p.Action("send", sdk.EffectWrite, send, sdk.Describe("send an email, with a calendar invite when start is set"))

	p.Action("notify.send", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		To    string `json:"to" rig:"required" desc:"addresses, comma separated"`
		Title string `json:"title"`
		Body  string `json:"body" rig:"required"`
	}) (*sdk.Result, error) {
		return send(c, sendIn{To: in.To, Subject: cmp.Or(in.Title, "Message from rig"), Body: in.Body})
	})

	p.Source("inbox", func(c *sdk.Ctx, in struct {
		Folder string `json:"folder" default:"INBOX"`
		Query  string `json:"query" desc:"IMAP search: UNSEEN, FROM \"x\", SINCE 1-Oct-2026"`
		Limit  int    `json:"limit" desc:"default 50"`
	}) (*sdk.Frame, error) {
		a, err := load(c)
		if err != nil {
			return nil, err
		}
		if in.Limit <= 0 {
			in.Limit = 50
		}
		rows, err := Inbox(a, cmp.Or(in.Folder, "INBOX"), in.Query, in.Limit)
		return frames.Of(rows), err
	}, sdk.Cache("60s"), sdk.Describe("the newest messages of a folder"))

	p.Source("message", func(c *sdk.Ctx, in struct {
		UID    string `json:"uid" rig:"required"`
		Folder string `json:"folder"`
	}) (*sdk.Frame, error) {
		a, err := load(c)
		if err != nil {
			return nil, err
		}
		m, err := Read(a, cmp.Or(in.Folder, "INBOX"), in.UID)
		if err != nil {
			return nil, err
		}
		h := m.Header
		md := fmt.Sprintf("**%s**\n\nFrom %s · %s\n\n---\n\n%s", decode(h.Get("Subject")), decode(h.Get("From")), h.Get("Date"), Text(m))
		return frames.Of([]struct {
			Markdown  string `json:"markdown"`
			MessageID string `json:"message_id"`
			From      string `json:"from"`
			Subject   string `json:"subject"`
		}{{md, h.Get("Message-Id"), decode(h.Get("From")), decode(h.Get("Subject"))}}), nil
	})
}

// ParseTime reads RFC 3339, a datetime-local value, or a date.
func ParseTime(s string) (time.Time, error) {
	for _, l := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("time %q: want 2026-10-27T10:00", s)
}
