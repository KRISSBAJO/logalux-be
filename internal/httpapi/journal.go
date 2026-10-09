package httpapi

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"logaluxe/api/internal/geo"
)

// The Journal: articles about beauty, read from the home screens and at /journal.
// Public routes show only what is live. An article is live when it is published,
// or scheduled for a time that has passed: the list filters on published_at, so
// nothing has to run to put a scheduled piece up.

// journalCategories are the sections of the Journal, in the order a screen shows them.
// business is for professionals; guide is how to use LogaLuxe.
var journalCategories = []struct{ Key, Label string }{
	{"hair", "Hair"}, {"braids", "Braids"}, {"barber", "Barber"}, {"nails", "Nails"}, {"lashes", "Lashes and brows"},
	{"skin", "Skin"}, {"makeup", "Makeup"}, {"spa", "Spa"}, {"business", "For professionals"}, {"guide", "Using LogaLuxe"},
}

func journalLabel(key string) string {
	for _, c := range journalCategories {
		if c.Key == key {
			return c.Label
		}
	}
	return key
}

func journalCategoryKeys() string {
	keys := make([]string, len(journalCategories))
	for i, c := range journalCategories {
		keys[i] = c.Key
	}
	return strings.Join(keys, ", ")
}

// journalCTA is the line under an article that sends the reader on to book, by category.
var journalCTA = map[string]string{
	"hair": "Find a hair stylist near you", "braids": "Find a braider near you", "barber": "Find a barber near you",
	"nails": "Find a nail technician near you", "lashes": "Find a lash technician near you", "skin": "Find a skin specialist near you",
	"makeup": "Find a makeup artist near you", "spa": "Find a spa near you", "business": "List your business on LogaLuxe", "guide": "Find a professional near you",
}

// journalLive is the condition for an article the public may read, on a table aliased "a".
const journalLive = `a.status in ('published','scheduled') and a.published_at is not null and a.published_at <= now()`

// articleListCols are the fields every list and card needs.
const articleListCols = `a.id, a.slug, a.title, a.dek, a.category, a.tags, a.author_name, a.author_role, a.author_media_id,
	a.cover_media_id, a.cover_alt, a.country, a.featured, a.reading_minutes, a.view_count, a.published_at`

// ---------- the pure parts ----------

// articleSlug makes a web address from a title: "Knotless braids: what to ask for" becomes knotless-braids-what-to-ask-for.
func articleSlug(title string) string { return slugify(title) }

// wordCount counts the words a reader will read: markdown marks, image addresses and bare punctuation do not count.
func wordCount(md string) int {
	md = mediaImageRe.ReplaceAllString(md, "")
	md = markdownLinkRe.ReplaceAllString(md, "$1")
	n := 0
	for _, f := range strings.Fields(md) {
		if strings.ContainsAny(f, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
			n++
		}
	}
	return n
}

// readingMinutes is the body's length at 220 words a minute, rounded to the nearest minute and never under one.
func readingMinutes(md string) int {
	m := (wordCount(md) + 110) / 220
	if m < 1 {
		m = 1
	}
	return m
}

var (
	// ![alt](media:<id>): a picture uploaded through the admin, kept by id so it survives a move of storage.
	mediaImageRe = regexp.MustCompile(`\]\(media:([0-9a-fA-F-]{36})\)`)
	// The same picture pasted as an address, which an editor does by habit; normalised back to media:<id> on save.
	mediaURLRe     = regexp.MustCompile(`\]\((?:https?://[^/\s)]+)?(?:/v1)?/media/([0-9a-fA-F-]{36})\)`)
	markdownLinkRe = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// rewriteMediaImages turns every ![alt](media:<id>) into ![alt](<base>/<id>), for a reader that cannot resolve ids itself.
func rewriteMediaImages(md, base string) string {
	base = strings.TrimRight(base, "/")
	return mediaImageRe.ReplaceAllString(md, "]("+base+"/$1)")
}

// canonicalMediaImages turns a pasted picture address back into media:<id>, so the stored body has one form.
func canonicalMediaImages(md string) string {
	return mediaURLRe.ReplaceAllString(md, "](media:$1)")
}

// mediaImageIDs lists the pictures a body refers to, each once, in order of first use.
func mediaImageIDs(md string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range mediaImageRe.FindAllStringSubmatch(md, -1) {
		id := strings.ToLower(m[1])
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ---------- shaping an article for the public ----------

// finishArticle adds the label, the professionals to show and the booking line, with their defaults.
func finishArticle(a M) M {
	cat, _ := a["category"].(string)
	a["category_label"] = journalLabel(cat)
	if _, has := a["related_category"]; has {
		related, _ := a["related_category"].(string)
		if related == "" && businessCategories[cat] {
			related = cat
		}
		if related == "" {
			a["related_category"] = nil
		} else {
			a["related_category"] = related
		}
		if cta, _ := a["cta_text"].(string); cta == "" {
			a["cta_text"] = firstNonEmpty(journalCTA[related], journalCTA[cat])
		}
	}
	return a
}

func finishArticles(list []M) []M {
	for _, a := range list {
		finishArticle(a)
	}
	return list
}

// countrySQL is the readers' country filter: an article for both countries, or for theirs.
const countrySQL = `($1 = '' or a.country = '' or a.country = $1)`

func journalCountry(w http.ResponseWriter, raw string) (string, bool) {
	c := strings.ToUpper(strings.TrimSpace(raw))
	if c != "" && !geo.Supported(c) {
		writeErr(w, 400, "country must be US, NG or left out for both")
		return "", false
	}
	return c, true
}

// settleScheduled marks scheduled articles whose time has come as published, so the admin list says what the public sees.
func settleScheduled(ctx context.Context, pool database) {
	_, _ = pool.Exec(ctx, `update articles set status='published' where status='scheduled' and published_at <= now()`)
}

// staffRequest reports whether the request carries a valid console token, without refusing it when it does not.
func (s *Server) staffRequest(r *http.Request) bool {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		return false
	}
	if s.cfg.AdminToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.AdminToken)) == 1 {
		return true
	}
	var ok bool
	_ = s.pool.QueryRow(r.Context(), `select exists(select 1 from admin_sessions s join admin_users a on a.id = s.admin_id where s.token_hash = $1 and s.expires_at > now() and a.active)`, hashToken(tok)).Scan(&ok)
	return ok
}

// ---------- public routes ----------

// GET /v1/journal?category=&country=&q=&tag=&limit=&offset=&featured=1
// Live articles, featured first then newest; with q, best match first. categories carries the counts
// for the chips under the same country and search, so a chip never leads to an empty page.
func (s *Server) journalList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	country, ok := journalCountry(w, q.Get("country"))
	if !ok {
		return
	}
	category := strings.TrimSpace(q.Get("category"))
	if category != "" && journalLabel(category) == category {
		writeErr(w, 400, "category must be one of "+journalCategoryKeys())
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 12
	}
	if limit > 500 {
		limit = 500
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	text := strings.TrimSpace(q.Get("q"))
	tag := strings.ToLower(strings.TrimSpace(q.Get("tag")))
	where := journalLive + ` and ` + countrySQL + ` and ($2 = '' or a.category = $2)
		and ($3 = '' or a.search @@ plainto_tsquery('english', $3)) and ($4 = '' or $4 = any(a.tags))`
	if q.Get("featured") == "1" {
		where += ` and a.featured`
	}
	order := `a.featured desc, a.published_at desc`
	if text != "" {
		order = `ts_rank(a.search, plainto_tsquery('english', $3)) desc, a.published_at desc`
	}
	args := []any{country, category, text, tag}
	out, err := rows(ctx, s.pool, `select `+articleListCols+`, count(*) over() as total from articles a where `+where+` order by `+order+`, a.sort, a.title limit `+strconv.Itoa(limit)+` offset `+strconv.Itoa(offset), args...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	total := 0
	for _, a := range out {
		total = int(toInt(a["total"]))
		delete(a, "total")
	}
	if len(out) == 0 && offset > 0 {
		_ = s.pool.QueryRow(ctx, `select count(*) from articles a where `+where, args...).Scan(&total)
	}
	// The chips: how many live pieces each section has, under the same country, search and tag.
	counts := map[string]int{}
	cs, _ := rows(ctx, s.pool, `select a.category, count(*)::int as n from articles a where `+journalLive+` and `+countrySQL+`
		and ($2 = '' or a.search @@ plainto_tsquery('english', $2)) and ($3 = '' or $3 = any(a.tags)) group by a.category`, country, text, tag)
	for _, c := range cs {
		counts[c["category"].(string)] = int(toInt(c["n"]))
	}
	categories := []M{}
	for _, c := range journalCategories {
		if counts[c.Key] > 0 {
			categories = append(categories, M{"key": c.Key, "label": c.Label, "count": counts[c.Key]})
		}
	}
	writeJSON(w, 200, M{"articles": finishArticles(out), "total": total, "categories": categories, "limit": limit, "offset": offset})
}

// GET /v1/journal/home?country=
// What the home screens show in one call: the featured piece (the newest when none is marked),
// the three newest after it, and how many live articles there are for the reader's country.
func (s *Server) journalHome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	country, ok := journalCountry(w, r.URL.Query().Get("country"))
	if !ok {
		return
	}
	var featured any
	first, err := row(ctx, s.pool, `select `+articleListCols+` from articles a where `+journalLive+` and `+countrySQL+` order by a.featured desc, a.published_at desc, a.sort limit 1`, country)
	seen := ""
	if err == nil {
		featured = finishArticle(first)
		seen = first["id"].(string)
	} else if !isNoRows(err) {
		writeErr(w, 500, err.Error())
		return
	}
	latest, err := rows(ctx, s.pool, `select `+articleListCols+` from articles a where `+journalLive+` and `+countrySQL+` and a.id::text <> $2 order by a.published_at desc, a.sort limit 3`, country, seen)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var count int
	_ = s.pool.QueryRow(ctx, `select count(*) from articles a where `+journalLive+` and `+countrySQL, country).Scan(&count)
	writeJSON(w, 200, M{"featured": featured, "latest": finishArticles(latest), "count": count})
}

// GET /v1/journal/{slug}?quiet=1&images=url
// One article with its body, three related pieces and the next one to read. Counts a view
// unless quiet=1. A piece that is not live answers 404 unless a staff token is sent (preview).
// images=url rewrites ![alt](media:<id>) to /v1/media/<id> for a reader with no renderer of its own.
func (s *Server) journalRead(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := chi.URLParam(r, "slug")
	a, err := row(ctx, s.pool, `select `+articleListCols+`, a.body_md, a.seo_title, a.seo_description, a.related_category, a.cta_text, a.status from articles a where a.slug = $1`, slug)
	if err != nil {
		if isNoRows(err) {
			writeErr(w, 404, "article not found")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	published, _ := a["published_at"].(time.Time)
	live := (a["status"] == "published" || a["status"] == "scheduled") && !published.IsZero() && !published.After(time.Now())
	if !live && !s.staffRequest(r) {
		writeErr(w, 404, "article not found")
		return
	}
	if live && a["status"] == "scheduled" {
		settleScheduled(ctx, s.pool)
		a["status"] = "published"
	}
	// A view is a person reading a live piece. A sitemap build or a preview is not.
	if live && r.URL.Query().Get("quiet") != "1" {
		var n int
		if s.pool.QueryRow(ctx, `update articles set view_count = view_count + 1 where id = $1 returning view_count`, a["id"]).Scan(&n) == nil {
			a["view_count"] = n
		}
	}
	if r.URL.Query().Get("images") == "url" {
		a["body_md"] = rewriteMediaImages(a["body_md"].(string), "/v1/media")
	}
	finishArticle(a)

	// Related: the same section first, then the newest, never this one.
	related, _ := rows(ctx, s.pool, `select `+articleListCols+` from articles a where `+journalLive+` and a.id <> $1
		order by (a.category = $2) desc, a.featured desc, a.published_at desc limit 3`, a["id"], a["category"])
	// Next: the piece published before this one; for a preview or the oldest piece, the newest.
	var next any
	at := published
	if at.IsZero() {
		at = time.Now()
	}
	n, err := row(ctx, s.pool, `select `+articleListCols+` from articles a where `+journalLive+` and a.id <> $1 and a.published_at < $2 order by a.published_at desc limit 1`, a["id"], at)
	if err != nil && len(related) > 0 {
		n, err = row(ctx, s.pool, `select `+articleListCols+` from articles a where `+journalLive+` and a.id <> $1 order by a.published_at desc limit 1`, a["id"])
	}
	if err == nil {
		next = finishArticle(n)
	}
	writeJSON(w, 200, M{"article": a, "related": finishArticles(related), "next": next})
}

// GET /v1/journal/{slug}/professionals?lat=&lng=&place=&scope=
// The four professionals to show under the article: the same search as GET /v1/businesses with the
// article's related category, fill=4 and nearest first, so the answer renders with the existing cards.
func (s *Server) journalProfessionals(w http.ResponseWriter, r *http.Request) {
	var category, related, status string
	var published *time.Time
	if err := s.pool.QueryRow(r.Context(), `select category, coalesce(related_category, ''), status, published_at from articles where slug = $1`, chi.URLParam(r, "slug")).Scan(&category, &related, &status, &published); err != nil {
		writeErr(w, 404, "article not found")
		return
	}
	live := (status == "published" || status == "scheduled") && published != nil && !published.After(time.Now())
	if !live && !s.staffRequest(r) {
		writeErr(w, 404, "article not found")
		return
	}
	if related == "" && businessCategories[category] {
		related = category
	}
	q := url.Values{}
	for _, k := range []string{"lat", "lng", "place", "scope", "market", "unit", "radius", "quiet"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	if related != "" {
		q.Set("category", related)
	}
	q.Set("fill", "4")
	q.Set("limit", "4")
	inner := r.Clone(r.Context())
	inner.URL = &url.URL{Path: "/v1/businesses", RawQuery: q.Encode()}
	rec := &capture{code: 200, header: http.Header{}}
	s.listBusinesses(rec, inner)
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.code)
	_, _ = w.Write(rec.body.Bytes())
}
