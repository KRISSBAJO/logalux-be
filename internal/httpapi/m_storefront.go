package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// The storefront: how the business looks on its public page. Its photos,
// its words, what it chooses to show, and how it answers its reviews.

// GET /v1/m/storefront
func (s *Server) mStorefront(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	b, err := row(ctx, s.pool, `select id, slug, name, tagline, about, category, phone, instagram, tiktok, website, tone, highlights, status, verification_status, rating, review_count,
		(select sm.id from site_media sm where sm.slot='logo' and sm.ref = businesses.slug limit 1) as logo_id from businesses where id=$1`, m.BusinessID)
	if err != nil {
		writeErr(w, 404, "business not found")
		return
	}
	photos, _ := rows(ctx, s.pool, `select id, alt, active, sort, size_bytes, created_at from site_media where slot='business' and ref=$1 order by sort, created_at`, m.Slug)
	reviews, reviewsPage, reviewsErr := s.historyRows(r, "reviews", `select id, author_name, service_name, rating, body, reply, pinned, status, created_at, replied_at from reviews where business_id=$1 and status in ('published','flagged') order by pinned desc, (reply = '') desc, created_at desc`, "pinned desc, (reply = '') desc, created_at desc", "created_at author_name rating status pinned", m.BusinessID)
	if reviewsErr != nil {
		writeErr(w, 500, reviewsErr.Error())
		return
	}
	reviewStats, _ := row(ctx, s.pool, `select count(*) filter(where status='published' and reply='') as unreplied from reviews where business_id=$1`, m.BusinessID)
	pinnedReview, _ := row(ctx, s.pool, `select id, body, author_name from reviews where business_id=$1 and pinned and status in ('published','flagged') order by created_at desc, id limit 1`, m.BusinessID)
	loc, _ := row(ctx, s.pool, `select name, address, city, region, hours, arrival_notes from locations where business_id=$1 and is_primary`, m.BusinessID)
	writeJSON(w, 200, M{"business": b, "display": s.bizSettings(ctx, m.BusinessID)["storefront"], "photos": photos, "reviews": reviews, "review_stats": reviewStats, "pinned_review": pinnedReview, "reviews_pagination": reviewsPage, "location": loc,
		"storage": s.store != nil, "url": strings.TrimRight(s.cfg.WebURL, "/") + "/b/" + m.Slug, "max_photos": mediaSlots["business"].Max})
}

// PUT /v1/m/storefront
func (s *Server) mStorefrontUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		Name       string         `json:"name"`
		Slug       string         `json:"slug"`
		Tagline    string         `json:"tagline"`
		About      string         `json:"about"`
		Category   string         `json:"category"`
		Highlights []string       `json:"highlights"`
		Instagram  string         `json:"instagram"`
		TikTok     string         `json:"tiktok"`
		Website    string         `json:"website"`
		Tone       string         `json:"tone"`
		Display    map[string]any `json:"display"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Name, req.Slug, req.Website = strings.TrimSpace(req.Name), strings.ToLower(strings.TrimSpace(req.Slug)), strings.TrimSpace(req.Website)
	switch {
	case len(req.Name) < 2 || len(req.Name) > 80:
		writeErr(w, 400, "the business name must be 2 to 80 characters")
		return
	case !slugRe.MatchString(req.Slug):
		writeErr(w, 400, "the handle can use lower-case letters, numbers and dashes, and must be at least two characters")
		return
	case !businessCategories[req.Category]:
		writeErr(w, 400, "choose a category")
		return
	case len(req.Tagline) > 140 || len(req.About) > 2000:
		writeErr(w, 400, "keep the tagline under 140 characters and the about text under 2,000")
		return
	case req.Tone != "" && !toneRe.MatchString(req.Tone):
		writeErr(w, 400, "the colour must look like #7A1F2B")
		return
	case req.Website != "" && !strings.HasPrefix(req.Website, "https://") && !strings.HasPrefix(req.Website, "http://"):
		writeErr(w, 400, "the website must start with https://")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `update businesses set name=$2, slug=$3, tagline=$4, about=$5, category=$6, highlights=$7, instagram=$8, tiktok=$9, website=$10, tone=coalesce(nullif($11,''), tone) where id=$1`,
		m.BusinessID, req.Name, req.Slug, strings.TrimSpace(req.Tagline), strings.TrimSpace(req.About), req.Category, cleanList(req.Highlights, 6), strings.TrimSpace(req.Instagram), strings.TrimSpace(req.TikTok), req.Website, req.Tone); err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "that handle is taken; choose another")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if req.Slug != m.Slug { // photos and shop products are filed under the handle
		_, _ = tx.Exec(ctx, `update site_media set ref=$2 where slot in ('business','logo') and ref=$1`, m.Slug, req.Slug)
	}
	_, _ = tx.Exec(ctx, `update products set seller_name=$2 where business_id=$1`, m.BusinessID, req.Name)
	if req.Display != nil {
		// Only known display switches are kept, each with the right type.
		defaults := defaultSettings()["storefront"]
		clean := map[string]any{}
		for k, def := range defaults {
			v, ok := req.Display[k]
			if !ok {
				continue
			}
			switch def.(type) {
			case bool:
				if b, ok := v.(bool); ok {
					clean[k] = b
				}
			case string:
				if str, ok := v.(string); ok && len(str) <= 200 {
					clean[k] = strings.TrimSpace(str)
				}
			}
		}
		raw, _ := json.Marshal(clean)
		if _, err := tx.Exec(ctx, `update businesses set settings = jsonb_set(settings, '{storefront}', coalesce(settings->'storefront','{}'::jsonb) || $2::jsonb) where id=$1`, m.BusinessID, string(raw)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true, "slug": req.Slug})
}

// POST /v1/m/storefront/photos   multipart: file, alt
func (s *Server) mPhotoUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	if s.store == nil {
		writeErr(w, 503, "photo storage is not set up yet; contact LogaLuxe support")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+(1<<20))
	if err := r.ParseMultipartForm(maxImageBytes + (1 << 20)); err != nil {
		writeErr(w, 413, "the photo is too large; the limit is 8 MB")
		return
	}
	alt := strings.TrimSpace(r.FormValue("alt"))
	if len(alt) > 200 {
		writeErr(w, 400, "keep the description under 200 characters")
		return
	}
	var have int
	_ = s.pool.QueryRow(ctx, `select count(*) from site_media where slot='business' and ref=$1`, m.Slug).Scan(&have)
	if have >= 40 {
		writeErr(w, 409, "you have 40 photos already; delete one to add another")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "choose a photo to upload")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil || len(data) == 0 {
		writeErr(w, 400, "the photo could not be read")
		return
	}
	if len(data) > maxImageBytes {
		writeErr(w, 413, "the photo is too large; the limit is 8 MB")
		return
	}
	contentType := http.DetectContentType(data) // trust the bytes, not the file name
	ext, ok := imageExt[contentType]
	if !ok {
		writeErr(w, 415, "use a JPEG, PNG or WebP photo")
		return
	}
	var id string
	if err := s.pool.QueryRow(ctx, `select gen_random_uuid()::text`).Scan(&id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	key := "site/business/" + m.Slug + "/" + id + ext
	if err := s.store.Put(ctx, key, contentType, data); err != nil {
		writeErr(w, 502, "the upload failed; try again")
		return
	}
	if _, err := s.pool.Exec(ctx, `insert into site_media (id, slot, ref, storage_key, content_type, size_bytes, alt, uploaded_by, sort)
		values ($1,'business',$2,$3,$4,$5,$6,$7, coalesce((select max(sort)+1 from site_media where slot='business' and ref=$2), 0))`, id, m.Slug, key, contentType, len(data), alt, m.Email); err != nil {
		_ = s.store.Delete(ctx, key)
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// PUT /v1/m/storefront/photos   {ids: [...]}   the new order; the first is the cover
func (s *Server) mPhotoOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil || len(req.IDs) == 0 || len(req.IDs) > 60 {
		writeErr(w, 400, "send the photos in their new order")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update site_media sm set sort = o.pos from unnest($2::uuid[]) with ordinality as o(id, pos) where sm.id = o.id and sm.slot='business' and sm.ref=$1`, mc(r).Slug, req.IDs); err != nil {
		writeErr(w, 400, "could not save that order")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// PUT /v1/m/storefront/photos/{id}   {alt, cover}
func (s *Server) mPhotoUpdate(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Alt   *string `json:"alt"`
		Cover bool    `json:"cover"`
	}
	if err := readJSON(r, &req); err != nil || (req.Alt != nil && len(*req.Alt) > 200) {
		writeErr(w, 400, "keep the description under 200 characters")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `update site_media set alt = coalesce($3, alt),
		sort = case when $4 then (select coalesce(min(sort),0) - 1 from site_media where slot='business' and ref=$2) else sort end
		where id=$1 and slot='business' and ref=$2`, chi.URLParam(r, "id"), m.Slug, req.Alt, req.Cover)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "photo not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// DELETE /v1/m/storefront/photos/{id}
func (s *Server) mPhotoDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var key string
	if err := s.pool.QueryRow(ctx, `select storage_key from site_media where id=$1 and slot='business' and ref=$2`, chi.URLParam(r, "id"), m.Slug).Scan(&key); err != nil {
		writeErr(w, 404, "photo not found")
		return
	}
	if s.store != nil {
		if err := s.store.Delete(ctx, key); err != nil {
			writeErr(w, 502, "the photo could not be deleted; try again")
			return
		}
	}
	_, _ = s.pool.Exec(ctx, `delete from site_media where storage_key=$1`, key)
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/reviews/{id}   {reply} or {pinned}
func (s *Server) mReviewUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req struct {
		Reply  *string `json:"reply"`
		Pinned *bool   `json:"pinned"`
	}
	if err := readJSON(r, &req); err != nil || (req.Reply != nil && len(*req.Reply) > 1000) {
		writeErr(w, 400, "keep the reply under 1,000 characters")
		return
	}
	var exists bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from reviews where id=$1 and business_id=$2)`, id, m.BusinessID).Scan(&exists)
	if !exists {
		writeErr(w, 404, "review not found")
		return
	}
	if req.Reply != nil {
		_, _ = s.pool.Exec(ctx, `update reviews set reply=$2, replied_at = case when $2 = '' then null else now() end where id=$1`, id, strings.TrimSpace(*req.Reply))
	}
	if req.Pinned != nil {
		if *req.Pinned { // one pinned review at a time
			_, _ = s.pool.Exec(ctx, `update reviews set pinned = (id::text = $2) where business_id=$1`, m.BusinessID, id)
		} else {
			_, _ = s.pool.Exec(ctx, `update reviews set pinned=false where id=$1`, id)
		}
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/auth/bookings/{id}/review   {rating, body}
// A client reviews a visit they completed. One review per visit.
func (s *Server) authReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := currentCustomer(r)
	id := chi.URLParam(r, "id")
	var req struct {
		Rating int    `json:"rating"`
		Body   string `json:"body"`
	}
	if !needVerified(w, c) {
		return
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Rating < 1 || req.Rating > 5 || len(req.Body) < 10 || len(req.Body) > 1500 {
		writeErr(w, 400, "choose one to five stars and write at least a sentence")
		return
	}
	var bizID, status, service string
	if err := s.pool.QueryRow(ctx, `select bk.business_id::text, bk.status, coalesce((select name from booking_items where booking_id = bk.id limit 1), '') from bookings bk where bk.id=$1 and bk.user_id=$2`, id, c.ID).Scan(&bizID, &status, &service); err != nil {
		writeErr(w, 404, "booking not found")
		return
	}
	var forbidden bool
	if err := s.pool.QueryRow(ctx, `select is_internal or customer_business_member(user_id,business_id) from bookings where id=$1`, id).Scan(&forbidden); err != nil {
		writeErr(w, 500, "could not verify review eligibility")
		return
	}
	if forbidden {
		writeErr(w, 403, "owners and staff cannot review their own business")
		return
	}
	if status != "paid" && status != "completed" {
		writeErr(w, 409, "you can review a visit once it is finished")
		return
	}
	author := strings.TrimSpace(c.FirstName + " " + firstInitial(c.LastName))
	if _, err := s.pool.Exec(ctx, `insert into reviews (business_id, booking_id, author_name, service_name, rating, body) values ($1,$2,$3,$4,$5,$6)`, bizID, id, author, service, req.Rating, req.Body); err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "you have already reviewed this visit")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.pool.Exec(ctx, `update businesses b set rating = coalesce((select round(avg(rating),2) from reviews where business_id=b.id and status='published'),0),
		review_count = (select count(*) from reviews where business_id=b.id and status='published') where b.id=$1`, bizID)
	writeJSON(w, 201, M{"ok": true})
}

func firstInitial(s string) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > 0 {
		return strings.ToUpper(string(r[0])) + "."
	}
	return ""
}
