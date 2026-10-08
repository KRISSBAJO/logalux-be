package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Getting a new business ready to take bookings: what is done, what is left,
// and the papers LogaLuxe needs to check who runs it.

const maxDocBytes = 10 << 20 // 10 MB

var docTypes = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "application/pdf": ".pdf"}
var docKinds = map[string]string{"id": "Government photo ID", "licence": "Professional licence", "address": "Proof of business address"}

// GET /v1/m/onboarding   the setup steps for this business, each done or not, worked out from what is really there
func (s *Server) mOnboarding(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	f, err := row(ctx, s.pool, `select b.status, b.verification_status, b.setup_dismissed_at,
		(coalesce(b.tagline,'') <> '' and coalesce(b.about,'') <> '') as has_profile,
		exists(select 1 from locations l where l.business_id = b.id and coalesce(l.address,'') <> '') as has_address,
		exists(select 1 from locations l, jsonb_each(l.hours) h where l.business_id = b.id and h.value <> 'null'::jsonb) as has_hours,
		(select count(*) from services sv where sv.business_id = b.id and sv.online and not sv.archived) as services,
		(select count(*) from staff st where st.business_id = b.id and st.bookable and not st.archived
		   and exists (select 1 from staff_services ss where ss.staff_id = st.id)) as staff_ready,
		(select count(*) from site_media sm where sm.slot = 'business' and sm.ref = b.slug and sm.active) as photos,
		exists(select 1 from payout_accounts pa where pa.business_id = b.id) as has_payout,
		(select count(*) from verification_documents vd where vd.business_id = b.id) as documents,
		(select vr.status from verification_requests vr where vr.business_id = b.id order by vr.created_at desc limit 1) as request_status,
		(select vr.submitted_at from verification_requests vr where vr.business_id = b.id order by vr.created_at desc limit 1) as submitted_at,
		(select vr.decision_note from verification_requests vr where vr.business_id = b.id order by vr.created_at desc limit 1) as decision_note
		from businesses b where b.id=$1`, m.BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	yes := func(k string) bool { v, _ := f[k].(bool); return v }
	verified := f["verification_status"] == "verified"
	submitted := f["submitted_at"] != nil && f["request_status"] == "pending"
	steps := []M{
		{"key": "profile", "title": "Describe your business", "hint": "A one-line tagline and a few sentences about what you do.", "href": "/business/storefront", "done": yes("has_profile")},
		{"key": "address", "title": "Add your address and opening hours", "hint": "Clients see where you are and when you are open; bookings follow your hours.", "href": "/business/settings", "done": yes("has_address") && yes("has_hours")},
		{"key": "services", "title": "Add your services and prices", "hint": "At least one service clients can book online.", "href": "/business/services", "done": toInt(f["services"]) > 0},
		{"key": "staff", "title": "Say who does what", "hint": "At least one person who takes bookings, with the services they do.", "href": "/business/staff", "done": toInt(f["staff_ready"]) > 0},
		{"key": "photos", "title": "Add photos of your work", "hint": "Clients want to see your work before they book.", "href": "/business/storefront", "done": toInt(f["photos"]) > 0},
		{"key": "payout", "title": "Say where to send your money", "hint": "The bank account your payouts go to.", "href": "/business/money/payout-account", "done": yes("has_payout")},
		{"key": "verify", "title": "Confirm who you are", "hint": "Upload a photo ID so LogaLuxe can check it. Your page goes live once it is approved.", "href": "/business/setup#verify", "done": verified || submitted},
	}
	done := 0
	for _, st := range steps {
		if st["done"] == true {
			done++
		}
	}
	docs, _ := rows(ctx, s.pool, `select id, kind, file_name, size_bytes, created_at from verification_documents where business_id=$1 order by created_at desc`, m.BusinessID)
	writeJSON(w, 200, M{"steps": steps, "done": done, "total": len(steps), "status": f["status"], "verification": M{
		"status": f["verification_status"], "request_status": f["request_status"], "submitted_at": f["submitted_at"], "decision_note": f["decision_note"], "documents": docs, "kinds": docKinds},
		"live": f["status"] == "live", "dismissed": f["setup_dismissed_at"] != nil})
}

// POST /v1/m/onboarding/dismiss   hide the setup list from Home once the owner is done with it
func (s *Server) mOnboardingDismiss(w http.ResponseWriter, r *http.Request) {
	_, _ = s.pool.Exec(r.Context(), `update businesses set setup_dismissed_at = now() where id=$1`, mc(r).BusinessID)
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/verification/documents   multipart: kind (id | licence | address), file (JPEG, PNG, WebP or PDF, 10 MB)
func (s *Server) mVerificationUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	if s.store == nil {
		writeErr(w, 503, "document storage is not set up yet; contact LogaLuxe support")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDocBytes+(1<<20))
	if err := r.ParseMultipartForm(maxDocBytes + (1 << 20)); err != nil {
		writeErr(w, 413, "the file is too large; the limit is 10 MB")
		return
	}
	kind := r.FormValue("kind")
	if docKinds[kind] == "" {
		writeErr(w, 400, "say what the document is: a photo ID, a licence, or proof of address")
		return
	}
	file, head, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "choose a file to upload")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxDocBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxDocBytes {
		writeErr(w, 413, "the file could not be read, or is over 10 MB")
		return
	}
	contentType := http.DetectContentType(data) // trust the bytes, not the file name
	ext, ok := docTypes[contentType]
	if !ok {
		writeErr(w, 415, "use a photo (JPEG, PNG or WebP) or a PDF")
		return
	}
	var n int
	_ = s.pool.QueryRow(ctx, `select count(*) from verification_documents where business_id=$1`, m.BusinessID).Scan(&n)
	if n >= 10 {
		writeErr(w, 409, "you have uploaded 10 documents, which is the most we keep; remove one first")
		return
	}
	var id string
	_ = s.pool.QueryRow(ctx, `select gen_random_uuid()::text`).Scan(&id)
	key := "private/verification/" + m.BusinessID + "/" + id + ext
	if err := s.store.Put(ctx, key, contentType, data); err != nil {
		writeErr(w, 502, "the upload failed; try again")
		return
	}
	name := path.Base(strings.ReplaceAll(head.Filename, "\\", "/"))
	if len(name) > 120 {
		name = name[len(name)-120:]
	}
	if _, err := s.pool.Exec(ctx, `insert into verification_documents (id, business_id, kind, file_name, storage_key, content_type, size_bytes, uploaded_by) values ($1,$2,$3,$4,$5,$6,$7,$8)`,
		id, m.BusinessID, kind, name, key, contentType, len(data), m.Email); err != nil {
		_ = s.store.Delete(ctx, key)
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// DELETE /v1/m/verification/documents/{id}   only before the papers have been sent for checking, or after staff asked for more
func (s *Server) mVerificationDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var key string
	if err := s.pool.QueryRow(ctx, `delete from verification_documents vd where vd.id::text=$1 and vd.business_id=$2
		and not exists (select 1 from verification_requests vr where vr.business_id = vd.business_id and vr.submitted_at is not null and vr.status in ('pending','approved'))
		returning vd.storage_key`, chi.URLParam(r, "id"), m.BusinessID).Scan(&key); err != nil {
		writeErr(w, 409, "this document is with LogaLuxe for checking and cannot be removed now")
		return
	}
	if s.store != nil {
		_ = s.store.Delete(ctx, key)
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/verification/submit   {id_type, note}   sends the papers to LogaLuxe staff. A photo ID is required.
func (s *Server) mVerificationSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		IDType string `json:"id_type"`
		Note   string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.IDType, req.Note = strings.TrimSpace(req.IDType), strings.TrimSpace(req.Note)
	idTypes := map[string]bool{"Driver's licence": true, "Passport": true, "National ID card": true, "Voter's card": true, "State ID": true}
	if !idTypes[req.IDType] {
		writeErr(w, 400, "say which kind of ID you uploaded")
		return
	}
	if len(req.Note) > 1000 {
		writeErr(w, 400, "keep the note under 1,000 characters")
		return
	}
	var hasID bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from verification_documents where business_id=$1 and kind='id')`, m.BusinessID).Scan(&hasID)
	if !hasID {
		writeErr(w, 409, "upload a photo of your ID first")
		return
	}
	var status string
	_ = s.pool.QueryRow(ctx, `select verification_status from businesses where id=$1`, m.BusinessID).Scan(&status)
	if status == "verified" {
		writeErr(w, 409, "this business is already verified")
		return
	}
	// One request per business is kept open: the one made at sign-up, or a new one after a refusal.
	tag, err := s.pool.Exec(ctx, `update verification_requests set status='pending', id_type=$2, portfolio_note=$3, submitted_at=now(), decision_note=''
		where id = (select id from verification_requests where business_id=$1 and status in ('pending','needs_info') order by created_at desc limit 1)`, m.BusinessID, req.IDType, firstNonEmpty(req.Note, "Papers uploaded by the business"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.pool.Exec(ctx, `insert into verification_requests (business_id, status, id_type, portfolio_note, submitted_at) values ($1,'pending',$2,$3,now())`, m.BusinessID, req.IDType, firstNonEmpty(req.Note, "Papers uploaded by the business")); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	_, _ = s.pool.Exec(ctx, `update businesses set verification_status='pending' where id=$1 and verification_status in ('unverified','rejected')`, m.BusinessID)
	writeJSON(w, 200, M{"ok": true})
}

// GET /v1/admin/verification/{id}/documents   the papers behind one request
func (s *Server) adminVerificationDocs(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select vd.id, vd.kind, vd.file_name, vd.content_type, vd.size_bytes, vd.uploaded_by, vd.created_at from verification_documents vd
		join verification_requests vr on vr.business_id = vd.business_id where vr.id::text=$1 order by vd.created_at`, chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"documents": out, "kinds": docKinds})
}

// GET /v1/admin/verification-documents/{id}   the file itself, for signed-in staff only. Each look is written to the audit log.
func (s *Server) adminVerificationDoc(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeErr(w, 503, "document storage is not configured")
		return
	}
	var key, contentType, name, biz string
	if err := s.pool.QueryRow(r.Context(), `select vd.storage_key, vd.content_type, vd.file_name, b.slug from verification_documents vd join businesses b on b.id = vd.business_id where vd.id::text=$1`, chi.URLParam(r, "id")).
		Scan(&key, &contentType, &name, &biz); err != nil {
		writeErr(w, 404, "document not found")
		return
	}
	res, err := s.store.Get(r.Context(), key)
	if err != nil {
		writeErr(w, 502, "could not read the document from storage")
		return
	}
	defer res.Body.Close()
	s.audit(r, "verification.document_viewed", biz, nil, M{"document": chi.URLParam(r, "id")})
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, strings.ReplaceAll(name, `"`, "")))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, res.Body)
}
