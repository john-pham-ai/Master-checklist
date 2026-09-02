package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultFeedbackTo is the recipient of help/bug/feedback emails.
const defaultFeedbackTo = "john.pham@applied.co"

var feedbackTypes = map[string]string{
	"help":     "Help request",
	"bug":      "Bug report",
	"feedback": "Feedback",
}

type feedbackService struct {
	cfg  config
	data *dataAPI
	tr   *translator
}

type feedbackRequest struct {
	Type    string `json:"type"`
	Subject string `json:"subject"`
	Message string `json:"message"`
	Lang    string `json:"lang"` // UI language at submit time: "en" | "ja"
	Page    string `json:"page"`
}

// setTridentCookie marks browser sessions so the platform's load balancer
// extension (Trident) injects X-Request-Token into subsequent requests, which
// the Data API needs to act as the signed-in user.
func setTridentCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: "trident", Value: "true", Path: "/",
		Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 60 * 60 * 24 * 30,
	})
}

func (s *feedbackService) handleForm(w http.ResponseWriter, r *http.Request) {
	setTridentCookie(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := feedbackTemplate.Execute(w, struct {
		CurrentEmail string
		FeedbackTo   string
	}{CurrentEmail: currentUserEmail(r), FeedbackTo: s.cfg.FeedbackTo})
	if err != nil {
		log.Printf("feedback template execute error: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// handleConnect starts the one-time Gmail (send-only) OAuth flow and returns
// the popup URL.
func (s *feedbackService) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cfg.DryRun {
		writeJSON(w, http.StatusOK, map[string]string{"url": "about:blank"})
		return
	}
	u, err := s.data.startOAuth(r.Context(), s.data.requestToken(r), "google-mail", []string{gmailSendScope})
	if err != nil {
		log.Printf("feedback: oauth start failed: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "oauth_start_failed", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": u})
}

func (s *feedbackService) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req feedbackRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_json"})
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	req.Subject = strings.TrimSpace(req.Subject)
	if _, ok := feedbackTypes[req.Type]; !ok {
		req.Type = "feedback"
	}
	if req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty_message"})
		return
	}

	ctx := r.Context()
	from := currentUserEmail(r)

	// Translate Japanese input to English for the recipient; failure is not
	// fatal — the original text is always included.
	var translatedMsg, translatedSubj, translateNote string
	if containsJapanese(req.Message) || containsJapanese(req.Subject) {
		tctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		if containsJapanese(req.Message) {
			if en, err := s.tr.toEnglish(tctx, req.Message); err != nil {
				log.Printf("feedback: translation failed: %v", err)
				translateNote = "Automatic translation was unavailable: " + truncate(err.Error(), 160)
			} else {
				translatedMsg = en
			}
		}
		if containsJapanese(req.Subject) && translateNote == "" {
			if en, err := s.tr.toEnglish(tctx, req.Subject); err == nil {
				translatedSubj = en
			}
		}
	}

	email := feedbackEmail{
		From:              from,
		To:                s.cfg.FeedbackTo,
		Type:              req.Type,
		Subject:           req.Subject,
		Message:           req.Message,
		TranslatedSubject: translatedSubj,
		TranslatedMessage: translatedMsg,
		TranslateNote:     translateNote,
		Lang:              req.Lang,
		Page:              req.Page,
		UserAgent:         r.UserAgent(),
		Environment:       s.cfg.URLBase,
		Revision:          os.Getenv("K_REVISION"),
		Now:               time.Now().UTC(),
	}
	raw := buildFeedbackEmail(email)

	if s.cfg.DryRun {
		log.Printf("[dry-run] would send feedback email:\n%s", raw)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "translated": translatedMsg != "", "dry_run": true})
		return
	}

	id, err := s.data.sendGmail(ctx, s.data.requestToken(r), raw)
	if err != nil {
		var de *dataAPIError
		if errors.As(err, &de) && de.needsConnect() {
			log.Printf("feedback: gmail not connected for %s: %v", from, err)
			writeJSON(w, http.StatusConflict, map[string]interface{}{"error": "gmail_not_connected", "needs_connect": true, "detail": truncate(de.Body, 200)})
			return
		}
		log.Printf("feedback: send failed for %s: %v", from, err)
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": "send_failed", "detail": truncate(err.Error(), 300)})
		return
	}
	log.Printf("feedback: %s from %s sent to %s (gmail id %s, translated=%v)", req.Type, from, s.cfg.FeedbackTo, id, translatedMsg != "")
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "translated": translatedMsg != "", "message_id": id})
}

type feedbackEmail struct {
	From, To          string
	Type              string
	Subject, Message  string
	TranslatedSubject string
	TranslatedMessage string
	TranslateNote     string
	Lang, Page        string
	UserAgent         string
	Environment       string
	Revision          string
	Now               time.Time
}

// buildFeedbackEmail renders an RFC 822 message (UTF-8, base64 body) suitable
// for the Gmail API's `raw` field.
func buildFeedbackEmail(e feedbackEmail) []byte {
	typeLabel := feedbackTypes[e.Type]
	if typeLabel == "" {
		typeLabel = "Feedback"
	}

	// English subject line: translated subject > original subject > first line of translation/message.
	headline := e.TranslatedSubject
	if headline == "" {
		headline = e.Subject
	}
	if headline == "" {
		src := e.TranslatedMessage
		if src == "" {
			src = e.Message
		}
		headline = strings.SplitN(src, "\n", 2)[0]
	}
	headline = truncateRunes(strings.TrimSpace(headline), 80)
	subject := fmt.Sprintf("[Master Checklist] %s: %s", typeLabel, headline)

	var b strings.Builder
	fmt.Fprintf(&b, "Master Checklist — %s\n", typeLabel)
	fmt.Fprintf(&b, "From:        %s\n", orDash(e.From))
	fmt.Fprintf(&b, "Submitted:   %s\n", e.Now.Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&b, "Environment: %s\n", orDash(strings.TrimSpace(e.Environment+" "+e.Revision)))
	fmt.Fprintf(&b, "UI language: %s\n", orDash(e.Lang))
	fmt.Fprintf(&b, "Page:        %s\n", orDash(e.Page))
	if e.Subject != "" {
		fmt.Fprintf(&b, "Subject:     %s\n", e.Subject)
		if e.TranslatedSubject != "" {
			fmt.Fprintf(&b, "             (EN) %s\n", e.TranslatedSubject)
		}
	}
	b.WriteString("\n")
	if e.TranslatedMessage != "" {
		b.WriteString("---- English translation (automatic) ----\n")
		b.WriteString(e.TranslatedMessage)
		b.WriteString("\n\n---- Original message ----\n")
	} else if e.TranslateNote != "" {
		b.WriteString("---- Note ----\n")
		b.WriteString(e.TranslateNote)
		b.WriteString("\n\n---- Original message ----\n")
	} else {
		b.WriteString("---- Message ----\n")
	}
	b.WriteString(e.Message)
	b.WriteString("\n")
	if e.UserAgent != "" {
		fmt.Fprintf(&b, "\n--\nUser agent: %s\n", e.UserAgent)
	}

	var msg strings.Builder
	if e.From != "" {
		fmt.Fprintf(&msg, "From: %s\r\n", e.From)
	}
	fmt.Fprintf(&msg, "To: %s\r\n", e.To)
	fmt.Fprintf(&msg, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&msg, "Date: %s\r\n", e.Now.Format(time.RFC1123Z))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	msg.WriteString("Content-Transfer-Encoding: base64\r\n")
	msg.WriteString("X-Master-Checklist-Type: " + e.Type + "\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(wrap76(base64.StdEncoding.EncodeToString([]byte(b.String()))))
	msg.WriteString("\r\n")
	return []byte(msg.String())
}

func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76])
		b.WriteString("\r\n")
		s = s[76:]
	}
	b.WriteString(s)
	return b.String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
