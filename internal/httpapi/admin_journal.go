package httpapi

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"logaluxe/api/internal/geo"
)

// The Journal's admin side. Every role reads; ops and above write; only a super
// admin publishes. An AI draft only ever fills the editor: a person reads it,
// edits it and publishes it, or does not.

var articleSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,119}$`)

const publishMinWords = 300

// articleReq is the editable part of an article, as the editor sends it. A PUT replaces all of it.
type articleReq struct {
	Title           string   `json:"title"`
	Slug            string   `json:"slug"`
	Dek             string   `json:"dek"`
	BodyMD          string   `json:"body_md"`
	CoverMediaID    *string  `json:"cover_media_id"`
	CoverAlt        string   `json:"cover_alt"`
	Category        string   `json:"category"`
	Tags            []string `json:"tags"`
	AuthorName      string   `json:"author_name"`
	AuthorRole      string   `json:"author_role"`
	AuthorMediaID   *string  `json:"author_media_id"`
	Country         string   `json:"country"`
	Featured        bool     `json:"featured"`
	Sort            int      `json:"sort"`
	RelatedCategory *string  `json:"related_category"`
	CTAText         string   `json:"cta_text"`
	SEOTitle        string   `json:"seo_title"`
	SEODescription  string   `json:"seo_description"`
}

// clean tidies what was typed and returns the first thing wrong with it, as a sentence.
func (s *Server) cleanArticle(r *http.Request, req *articleReq) string {
	ctx := r.Context()
	req.Title = strings.Join(strings.Fields(req.Title), " ")
	req.Dek = strings.Join(strings.Fields(req.Dek), " ")
	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))
	if req.Slug == "" {
		req.Slug = articleSlug(req.Title)
	}
	req.BodyMD = canonicalMediaImages(strings.ReplaceAll(strings.TrimSpace(req.BodyMD), "\r\n", "\n"))
	req.Country = strings.ToUpper(strings.TrimSpace(req.Country))
	req.AuthorName = firstNonEmpty(strings.TrimSpace(req.AuthorName), "LogaLuxe editorial")
	req.AuthorRole, req.CoverAlt, req.CTAText = strings.TrimSpace(req.AuthorRole), strings.TrimSpace(req.CoverAlt), strings.TrimSpace(req.CTAText)
	req.SEOTitle, req.SEODescription = strings.TrimSpace(req.SEOTitle), strings.TrimSpace(req.SEODescription)
	req.Tags = cleanList(req.Tags, 10)
	for i, t := range req.Tags {
		req.Tags[i] = strings.ToLower(t)
	}
	for _, p := range []**string{&req.CoverMediaID, &req.AuthorMediaID, &req.RelatedCategory} {
		if *p != nil && strings.TrimSpace(**p) == "" {
			*p = nil
		}
	}
	n := len([]rune(req.Title))
	switch {
	case n < 8 || n > 120:
		return "the title must be 8 to 120 characters"
	case len([]rune(req.Dek)) < 20 || len([]rune(req.Dek)) > 200:
		return "the summary under the title must be 20 to 200 characters"
	case !articleSlugRe.MatchString(req.Slug):
		return "the slug can only have lowercase letters, digits and hyphens, and must start with a letter or digit"
	case journalLabel(req.Category) == req.Category:
		return "category must be one of " + journalCategoryKeys()
	case req.Country != "" && !geo.Supported(req.Country):
		return "country must be US, NG or empty for both countries"
	case req.RelatedCategory != nil && !businessCategories[*req.RelatedCategory]:
		return "related_category must be a service category: hair, braids, barber, nails, lashes, skin, makeup or spa"
	case len([]rune(req.AuthorName)) > 80 || len([]rune(req.AuthorRole)) > 80:
		return "keep the author name and role under 80 characters each"
	case len([]rune(req.CoverAlt)) > 200:
		return "the cover description is too long; keep it under 200 characters"
	case len([]rune(req.CTAText)) > 60:
		return "the booking line must be under 60 characters"
	case len([]rune(req.SEOTitle)) > 70:
		return "the search title must be under 70 characters"
	case len([]rune(req.SEODescription)) > 200:
		return "the search description must be under 200 characters"
	case len(req.BodyMD) > 200000:
		return "the body is too long; keep it under 200,000 characters"
	}
	for _, t := range req.Tags {
		if len([]rune(t)) > 30 {
			return "keep each tag under 30 characters"
		}
	}
	for _, id := range []*string{req.CoverMediaID, req.AuthorMediaID} {
		if id == nil {
			continue
		}
		var ok bool
		if s.pool.QueryRow(ctx, `select exists(select 1 from site_media where id::text = $1)`, *id).Scan(&ok) != nil || !ok {
			return "that picture does not exist: " + *id
		}
	}
	for _, id := range mediaImageIDs(req.BodyMD) {
		var ok bool
		if s.pool.QueryRow(ctx, `select exists(select 1 from site_media where id::text = $1)`, id).Scan(&ok) != nil || !ok {
			return "a picture in the body does not exist: " + id
		}
	}
	return ""
}

// publishable says why an article cannot go live yet, or nothing.
func publishable(body string) string {
	if n := wordCount(body); n < publishMinWords {
		return "the article needs at least " + itoa(publishMinWords) + " words to publish; it has " + itoa(n)
	}
	return ""
}

// GET /v1/admin/journal?status=&q=
func (s *Server) adminJournal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settleScheduled(ctx, s.pool)
	q := r.URL.Query()
	status := q.Get("status")
	if status != "" && status != "draft" && status != "scheduled" && status != "published" && status != "archived" {
		writeErr(w, 400, "status must be draft, scheduled, published or archived")
		return
	}
	out, err := rows(ctx, s.pool, `select a.id, a.slug, a.title, a.dek, a.body_md, a.cover_media_id, a.cover_alt, a.category, a.tags, a.author_name, a.author_role, a.author_media_id,
		a.country, a.status, a.published_at, a.featured, a.sort, a.reading_minutes, a.view_count, a.related_category, a.cta_text, a.seo_title, a.seo_description,
		a.created_by, a.created_at, a.updated_at
		from articles a where ($1 = '' or a.status = $1) and ($2 = '' or a.title ilike '%'||$2||'%' or a.slug ilike '%'||$2||'%' or a.search @@ plainto_tsquery('english', $2))
		order by (a.status = 'draft') desc, a.updated_at desc`, status, strings.TrimSpace(q.Get("q")))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, a := range out {
		a["category_label"] = journalLabel(a["category"].(string))
	}
	writeJSON(w, 200, M{"articles": out})
}

// GET /v1/admin/journal/{id}   one article with every field, for the editor
func (s *Server) adminJournalOne(w http.ResponseWriter, r *http.Request) {
	a, err := row(r.Context(), s.pool, `select a.id, a.slug, a.title, a.dek, a.body_md, a.cover_media_id, a.cover_alt, a.category, a.tags, a.author_name, a.author_role, a.author_media_id,
		a.country, a.status, a.published_at, a.featured, a.sort, a.reading_minutes, a.view_count, a.related_category, a.cta_text, a.seo_title, a.seo_description,
		a.created_by, a.created_at, a.updated_at from articles a where a.id::text = $1`, chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, 404, "article not found")
		return
	}
	a["category_label"] = journalLabel(a["category"].(string))
	writeJSON(w, 200, M{"article": a})
}

// POST /v1/admin/journal   the editable fields → {ok, id, slug}   always a draft
func (s *Server) adminJournalCreate(w http.ResponseWriter, r *http.Request) {
	var req articleReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if msg := s.cleanArticle(r, &req); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	var id string
	err := s.pool.QueryRow(r.Context(), `insert into articles (slug, title, dek, body_md, cover_media_id, cover_alt, category, tags, author_name, author_role, author_media_id,
		country, featured, sort, reading_minutes, related_category, cta_text, seo_title, seo_description, created_by)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20) returning id::text`,
		req.Slug, req.Title, req.Dek, req.BodyMD, req.CoverMediaID, req.CoverAlt, req.Category, req.Tags, req.AuthorName, req.AuthorRole, req.AuthorMediaID,
		req.Country, req.Featured, req.Sort, readingMinutes(req.BodyMD), req.RelatedCategory, req.CTAText, req.SEOTitle, req.SEODescription, s.actor(r)).Scan(&id)
	if err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "that slug is already in use; choose another")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "journal.create", id, nil, M{"slug": req.Slug, "title": req.Title, "category": req.Category, "country": req.Country})
	writeJSON(w, 201, M{"ok": true, "id": id, "slug": req.Slug})
}

// PUT /v1/admin/journal/{id}   the same body; the slug can only change while the article is a draft
func (s *Server) adminJournalUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req articleReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	before, err := row(ctx, s.pool, `select slug, title, status, category, country, featured, published_at from articles where id::text = $1`, id)
	if err != nil {
		writeErr(w, 404, "article not found")
		return
	}
	if msg := s.cleanArticle(r, &req); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if before["status"] != "draft" && req.Slug != before["slug"] {
		writeErr(w, 400, "the slug cannot change once the article has been published; unpublish it first")
		return
	}
	_, err = s.pool.Exec(ctx, `update articles set slug=$2, title=$3, dek=$4, body_md=$5, cover_media_id=$6, cover_alt=$7, category=$8, tags=$9, author_name=$10, author_role=$11,
		author_media_id=$12, country=$13, featured=$14, sort=$15, reading_minutes=$16, related_category=$17, cta_text=$18, seo_title=$19, seo_description=$20, updated_at=now() where id::text = $1`,
		id, req.Slug, req.Title, req.Dek, req.BodyMD, req.CoverMediaID, req.CoverAlt, req.Category, req.Tags, req.AuthorName, req.AuthorRole,
		req.AuthorMediaID, req.Country, req.Featured, req.Sort, readingMinutes(req.BodyMD), req.RelatedCategory, req.CTAText, req.SEOTitle, req.SEODescription)
	if err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "that slug is already in use; choose another")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if req.Slug != before["slug"] { // pictures uploaded for the old slug follow the article
		_, _ = s.pool.Exec(ctx, `update site_media set ref=$2 where slot='article' and ref=$1`, before["slug"], req.Slug)
	}
	s.audit(r, "journal.update", id, before, M{"slug": req.Slug, "title": req.Title, "category": req.Category, "country": req.Country, "featured": req.Featured})
	writeJSON(w, 200, M{"ok": true, "id": id, "slug": req.Slug, "reading_minutes": readingMinutes(req.BodyMD)})
}

// POST /v1/admin/journal/{id}/publish   {at?: ISO time}   now, or at a later time (scheduled)
func (s *Server) adminJournalPublish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req struct {
		At string `json:"at"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, "invalid json")
			return
		}
	}
	before, err := row(ctx, s.pool, `select slug, status, published_at, body_md from articles where id::text = $1`, id)
	if err != nil {
		writeErr(w, 404, "article not found")
		return
	}
	if msg := publishable(before["body_md"].(string)); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	at := time.Now()
	status := "published"
	if strings.TrimSpace(req.At) != "" {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(req.At))
		if err != nil {
			writeErr(w, 400, "the time must be an ISO time, like 2026-11-01T09:00:00Z")
			return
		}
		if t.After(time.Now().Add(time.Minute)) {
			at, status = t, "scheduled"
		}
	}
	// "Now" must already be in the past on both clocks: lists ask the database whether an article is live and
	// the article's own page asks this server, and the two clocks are never exactly together. A scheduled time is kept as given.
	if err := s.pool.QueryRow(ctx, `update articles set status=$2, published_at = case when $2 = 'published' then least(now(), $3::timestamptz) else $3::timestamptz end, updated_at=now()
		where id::text = $1 returning published_at`, id, status, at).Scan(&at); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	delete(before, "body_md")
	s.audit(r, "journal.publish", id, before, M{"status": status, "published_at": at})
	writeJSON(w, 200, M{"ok": true, "status": status, "published_at": at})
}

// POST /v1/admin/journal/{id}/unpublish   back to a draft; the public address stops answering
func (s *Server) adminJournalUnpublish(w http.ResponseWriter, r *http.Request) {
	s.adminJournalSetStatus(w, r, "draft", "journal.unpublish")
}

// POST /v1/admin/journal/{id}/archive   kept, with its history, but no longer shown
func (s *Server) adminJournalArchive(w http.ResponseWriter, r *http.Request) {
	s.adminJournalSetStatus(w, r, "archived", "journal.archive")
}

func (s *Server) adminJournalSetStatus(w http.ResponseWriter, r *http.Request, status, action string) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	before, err := row(ctx, s.pool, `select slug, status, published_at from articles where id::text = $1`, id)
	if err != nil {
		writeErr(w, 404, "article not found")
		return
	}
	sql := `update articles set status=$2, updated_at=now() where id::text = $1`
	if status == "draft" {
		sql = `update articles set status=$2, published_at=null, updated_at=now() where id::text = $1`
	}
	if _, err := s.pool.Exec(ctx, sql, id, status); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, action, id, before, M{"status": status})
	writeJSON(w, 200, M{"ok": true, "status": status})
}

// DELETE /v1/admin/journal/{id}   drafts only; anything that was ever public is archived instead
func (s *Server) adminJournalDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	before, err := row(ctx, s.pool, `select slug, title, status from articles where id::text = $1`, id)
	if err != nil {
		writeErr(w, 404, "article not found")
		return
	}
	if before["status"] != "draft" {
		writeErr(w, 409, "only a draft can be deleted; archive it instead")
		return
	}
	if _, err := s.pool.Exec(ctx, `delete from articles where id::text = $1`, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "journal.delete", id, before, nil)
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/admin/journal/draft   {topic, category, country, notes}   → {title, dek, body_md, tags}
// A first draft in the house voice, written by the model. Nothing is saved: it fills the editor,
// and a person reads it, edits it and decides whether it is published.
func (s *Server) adminJournalDraft(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic    string `json:"topic"`
		Category string `json:"category"`
		Country  string `json:"country"`
		Notes    string `json:"notes"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Topic = strings.Join(strings.Fields(req.Topic), " ")
	req.Country = strings.ToUpper(strings.TrimSpace(req.Country))
	req.Notes = strings.TrimSpace(req.Notes)
	switch {
	case len([]rune(req.Topic)) < 5 || len([]rune(req.Topic)) > 200:
		writeErr(w, 400, "say in a few words what the article is about; 5 to 200 characters")
		return
	case journalLabel(req.Category) == req.Category:
		writeErr(w, 400, "category must be one of "+journalCategoryKeys())
		return
	case req.Country != "" && !geo.Supported(req.Country):
		writeErr(w, 400, "country must be US, NG or empty for both countries")
		return
	case len([]rune(req.Notes)) > 1500:
		writeErr(w, 400, "keep the notes under 1,500 characters")
		return
	}
	if s.cfg.OpenAIKey == "" {
		writeErr(w, 503, "AI drafting is not set up: add OPENAI_API_KEY to the API and restart it")
		return
	}
	readers := "readers in the United States and Nigeria"
	switch req.Country {
	case "US":
		readers = "readers in the United States (dollars, miles, American spelling of services)"
	case "NG":
		readers = "readers in Nigeria (naira, kilometres, Lagos and Abuja as the usual cities)"
	}
	system := `You write articles for the LogaLuxe Journal, a magazine inside a booking site for beauty professionals (hair, braids, barbers, nails, lashes and brows, skin, makeup, spa) in the United States and Nigeria. Rules:
- Plain, warm and precise. Short sentences and short paragraphs. Sentence case in headings and the title. No exclamation marks. No marketing words (no "ultimate", "stunning", "elevate", "unlock", "game-changer"). No em dashes; use a comma, a colon or a full stop.
- Be specific and useful: what to ask for, how long things take, what to do afterwards, what goes wrong and why. Write for someone about to book.
- Never invent a price, a statistic, a study or a named expert. If a number is needed and not given in the notes, say what it depends on instead.
- Do not say you are an AI. Do not add a sign-off.
- Length: 600 to 900 words. Structure: an opening paragraph, then sections under ## headings, lists where they help, and a closing section that tells the reader how to book on LogaLuxe (a sentence that links to /search?category=<category>).
- Answer with a JSON object: {"title": "...", "dek": "...", "body_md": "...", "tags": ["...", "..."]}. The dek is one sentence under the title, 20 to 200 characters. The body is Markdown. Tags are 3 to 6 short lowercase words.`
	link := "/search"
	if businessCategories[req.Category] {
		link += "?category=" + req.Category
	}
	user := "Topic: " + req.Topic + "\nSection: " + req.Category + " (" + journalLabel(req.Category) + ")\nWritten for " + readers + ".\nBooking link for the closing section: " + link
	if req.Notes != "" {
		user += "\nNotes from the editor (facts you may use; everything else must be general advice):\n" + req.Notes
	}
	raw, err := s.aiAsk(r.Context(), system, user, true)
	if err != nil {
		writeErr(w, 503, err.Error())
		return
	}
	var out struct {
		Title  string   `json:"title"`
		Dek    string   `json:"dek"`
		BodyMD string   `json:"body_md"`
		Tags   []string `json:"tags"`
	}
	if json.Unmarshal([]byte(raw), &out) != nil || strings.TrimSpace(out.BodyMD) == "" || strings.TrimSpace(out.Title) == "" {
		writeErr(w, 503, "the AI service returned something unusable; try again")
		return
	}
	s.audit(r, "journal.draft", req.Topic, nil, M{"category": req.Category, "country": req.Country, "words": wordCount(out.BodyMD)})
	writeJSON(w, 200, M{"title": strings.TrimSpace(out.Title), "dek": strings.TrimSpace(out.Dek), "body_md": strings.TrimSpace(out.BodyMD), "tags": cleanList(out.Tags, 6)})
}
