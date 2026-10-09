package httpapi

import (
	"io"
	"net/http"
)

// Only the signed-in merchant's own photo can be changed here.
func (s *Server) mProfilePhoto(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	if s.store == nil {
		writeErr(w, 503, "Photo storage is not configured")
		return
	}
	if r.Method == http.MethodDelete {
		if _, err := s.pool.Exec(ctx, `update merchant_users set photo_id=null where id=$1`, m.ID); err != nil {
			writeErr(w, 500, "Could not remove profile photo")
			return
		}
		writeJSON(w, 200, M{"ok": true})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+(1<<20))
	if err := r.ParseMultipartForm(maxImageBytes + (1 << 20)); err != nil {
		writeErr(w, 413, "Photo must be at most 8 MB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "Choose a photo")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImageBytes+1))
	if err != nil || len(data) == 0 {
		writeErr(w, 400, "Could not read photo")
		return
	}
	if len(data) > maxImageBytes {
		writeErr(w, 413, "Photo must be at most 8 MB")
		return
	}
	mime := http.DetectContentType(data)
	ext, ok := imageExt[mime]
	if !ok {
		writeErr(w, 415, "Use JPEG, PNG or WebP")
		return
	}
	var id string
	if err = s.pool.QueryRow(ctx, `select gen_random_uuid()::text`).Scan(&id); err != nil {
		writeErr(w, 500, "Could not prepare photo")
		return
	}
	key := "site/merchant/" + m.ID + "/" + id + ext
	if err = s.store.Put(ctx, key, mime, data); err != nil {
		writeErr(w, 502, "Upload failed; try again")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		_ = s.store.Delete(ctx, key)
		writeErr(w, 500, "Could not save photo")
		return
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `insert into site_media(id,slot,ref,storage_key,content_type,size_bytes,alt,uploaded_by) values($1,'merchant',$2,$3,$4,$5,'Profile photo',$6)`, id, m.ID, key, mime, len(data), m.Email)
	if err == nil {
		_, err = tx.Exec(ctx, `update merchant_users set photo_id=$2 where id=$1`, m.ID, id)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		_ = s.store.Delete(ctx, key)
		writeErr(w, 500, "Could not save photo")
		return
	}
	writeJSON(w, 201, M{"ok": true, "photo_id": id})
}
