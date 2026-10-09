package httpapi

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
)

// mediaSlot says where an uploaded image can appear on the public site.
// Ref names the one thing inside the slot that the image belongs to: a
// category, a business or a product. The hero has no ref.
type mediaSlot struct {
	Label string
	Max   int    // how many images may show at once for one ref
	Ref   string // "", "category", "business" or "product"
	Order int
}

var mediaSlots = map[string]mediaSlot{
	"hero":     {"Landing page hero", 6, "", 1},
	"category": {"Category tiles", 1, "category", 2},
	"business": {"Business photos", 8, "business", 3},
	"product":  {"Product photos", 6, "product", 4},
	"logo":     {"Business logos", 1, "business", 5},
	"article":  {"Journal pictures", 6, "article", 6},
}

var captionPositions = map[string]bool{"bottom-right": true, "bottom-left": true, "top-right": true, "top-left": true, "none": true}

var mediaCategories = map[string]bool{"hair": true, "braids": true, "barber": true, "nails": true, "lashes": true, "skin": true, "makeup": true, "spa": true}

const maxImageBytes = 8 << 20 // 8 MB

var (
	imageExt = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp"}
	refRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
)

// checkRef makes sure the image is being attached to something that exists.
func (s *Server) checkRef(ctx context.Context, slot mediaSlot, ref string) string {
	if slot.Ref == "" {
		if ref != "" {
			return "this slot does not take a ref"
		}
		return ""
	}
	if !refRe.MatchString(ref) {
		return "choose what the image belongs to"
	}
	var exists bool
	switch slot.Ref {
	case "category":
		exists = mediaCategories[ref]
	case "business":
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from businesses where slug=$1)`, ref).Scan(&exists)
	case "product":
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from products where slug=$1)`, ref).Scan(&exists)
	case "article":
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from articles where slug=$1)`, ref).Scan(&exists)
	}
	if !exists {
		return "no " + slot.Ref + " called " + ref
	}
	return ""
}

// GET /v1/site/media?slot=business&ref=ada
// Public: the images now showing. Leave ref out to get every ref in the slot,
// which lets a list page fetch all its pictures in one call.
func (s *Server) siteMedia(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	slot, ok := mediaSlots[q.Get("slot")]
	if !ok {
		writeJSON(w, 200, M{"media": []M{}})
		return
	}
	out, err := rows(r.Context(), s.pool, `
		select id, ref, alt, caption, caption_pos from (
		  select id, ref, alt, caption, caption_pos, sort, created_at, row_number() over (partition by ref order by sort, created_at) as n
		  from site_media where slot=$1 and active and ($2 = '' or ref = $2)
		) m where n <= $3 order by ref, sort, created_at`, q.Get("slot"), q.Get("ref"), slot.Max)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, M{"media": out})
}

// GET /v1/media/{id}   public: the image itself, streamed from the private bucket
func (s *Server) mediaFile(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeErr(w, 503, "image storage is not configured")
		return
	}
	var key, contentType string
	if err := s.pool.QueryRow(r.Context(), `select storage_key, content_type from site_media where id=$1`, chi.URLParam(r, "id")).Scan(&key, &contentType); err != nil {
		writeErr(w, 404, "image not found")
		return
	}
	res, err := s.store.Get(r.Context(), key)
	if err != nil {
		writeErr(w, 502, "could not read the image from storage")
		return
	}
	defer res.Body.Close()
	w.Header().Set("Content-Type", contentType)
	if n := res.Header.Get("Content-Length"); n != "" {
		w.Header().Set("Content-Length", n)
	}
	// An image never changes once uploaded: a replacement gets a new id.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, res.Body)
}

// GET /v1/admin/media?slot=&ref=
func (s *Server) adminMedia(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := rows(r.Context(), s.pool, `select id, slot, ref, alt, caption, caption_pos, active, sort, content_type, size_bytes, uploaded_by, created_at
		from site_media where ($1 = '' or slot = $1) and ($2 = '' or ref = $2) order by slot, ref, sort, created_at`, q.Get("slot"), q.Get("ref"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	slots := []M{}
	for key, v := range mediaSlots {
		slots = append(slots, M{"key": key, "label": v.Label, "max": v.Max, "ref": v.Ref, "order": v.Order})
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i]["order"].(int) < slots[j]["order"].(int) })
	writeJSON(w, 200, M{"media": out, "slots": slots, "storage": s.store != nil, "max_bytes": maxImageBytes})
}

// POST /v1/admin/media   multipart form: file, slot, ref, alt
func (s *Server) adminMediaUpload(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeErr(w, 503, "image storage is not configured: set AWS_REGION, AWS_S3_BUCKET, AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+(1<<20))
	if err := r.ParseMultipartForm(maxImageBytes + (1 << 20)); err != nil {
		writeErr(w, 413, "the image is too large; the limit is 8 MB")
		return
	}
	slotKey := r.FormValue("slot")
	slot, ok := mediaSlots[slotKey]
	if !ok {
		writeErr(w, 400, "unknown slot")
		return
	}
	ref := strings.TrimSpace(r.FormValue("ref"))
	if msg := s.checkRef(r.Context(), slot, ref); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	alt := strings.TrimSpace(r.FormValue("alt"))
	if len(alt) > 200 {
		writeErr(w, 400, "the description is too long; keep it under 200 characters")
		return
	}
	caption := strings.TrimSpace(r.FormValue("caption"))
	captionPos := r.FormValue("caption_pos")
	if captionPos == "" {
		captionPos = "bottom-right"
	}
	if len(caption) > 80 || !captionPositions[captionPos] {
		writeErr(w, 400, "keep the quote under 80 characters and choose a position for it")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "choose an image to upload")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil || len(data) == 0 {
		writeErr(w, 400, "the image could not be read")
		return
	}
	if len(data) > maxImageBytes {
		writeErr(w, 413, "the image is too large; the limit is 8 MB")
		return
	}
	// Trust the bytes, not the file name or the browser's claim.
	contentType := http.DetectContentType(data)
	ext, ok := imageExt[contentType]
	if !ok {
		writeErr(w, 415, "use a JPEG, PNG or WebP image")
		return
	}

	var id string
	if err := s.pool.QueryRow(r.Context(), `select gen_random_uuid()::text`).Scan(&id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	key := "site/" + slotKey + "/"
	if ref != "" {
		key += ref + "/"
	}
	key += id + ext
	if err := s.store.Put(r.Context(), key, contentType, data); err != nil {
		writeErr(w, 502, "upload to storage failed: "+err.Error())
		return
	}
	if _, err := s.pool.Exec(r.Context(), `insert into site_media (id, slot, ref, storage_key, content_type, size_bytes, alt, uploaded_by, caption, caption_pos, sort)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, coalesce((select max(sort)+1 from site_media where slot=$2 and ref=$3), 0))`,
		id, slotKey, ref, key, contentType, len(data), alt, s.actor(r), caption, captionPos); err != nil {
		_ = s.store.Delete(r.Context(), key) // do not leave an orphan in the bucket
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "media.upload", id, nil, M{"slot": slotKey, "ref": ref, "alt": alt, "bytes": len(data), "type": contentType})
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// PUT /v1/admin/media/{id}   {active, alt, sort}
func (s *Server) adminMediaUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Active     *bool   `json:"active"`
		Alt        *string `json:"alt"`
		Sort       *int    `json:"sort"`
		Caption    *string `json:"caption"`
		CaptionPos *string `json:"caption_pos"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Alt != nil && len(*req.Alt) > 200 {
		writeErr(w, 400, "the description is too long; keep it under 200 characters")
		return
	}
	if (req.Caption != nil && len(*req.Caption) > 80) || (req.CaptionPos != nil && !captionPositions[*req.CaptionPos]) {
		writeErr(w, 400, "keep the quote under 80 characters and choose a position for it")
		return
	}
	before, err := row(r.Context(), s.pool, `select active, alt, sort, caption, caption_pos from site_media where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "image not found")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update site_media set active=coalesce($2,active), alt=coalesce($3,alt), sort=coalesce($4,sort), caption=coalesce($5,caption), caption_pos=coalesce($6,caption_pos) where id=$1`, id, req.Active, req.Alt, req.Sort, req.Caption, req.CaptionPos); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "media.update", id, before, req)
	writeJSON(w, 200, M{"ok": true})
}

// DELETE /v1/admin/media/{id}
func (s *Server) adminMediaDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var key string
	before, err := row(r.Context(), s.pool, `select slot, ref, alt, size_bytes from site_media where id=$1`, id)
	if err == nil {
		err = s.pool.QueryRow(r.Context(), `select storage_key from site_media where id=$1`, id).Scan(&key)
	}
	if err != nil {
		writeErr(w, 404, "image not found")
		return
	}
	// A Journal cover can share its file with a hero photo: the file goes only when no other row still uses it.
	var shared bool
	_ = s.pool.QueryRow(r.Context(), `select exists(select 1 from site_media where storage_key=$1 and id<>$2)`, key, id).Scan(&shared)
	if s.store != nil && !shared {
		if err := s.store.Delete(r.Context(), key); err != nil {
			writeErr(w, 502, "could not delete the image from storage: "+err.Error())
			return
		}
	}
	if _, err := s.pool.Exec(r.Context(), `delete from site_media where id=$1`, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "media.delete", id, before, nil)
	writeJSON(w, 200, M{"ok": true})
}
