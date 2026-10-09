-- The AI-written "what people say" summary reaches the public page only after a person approves it.
-- A regenerated summary starts unapproved again.
alter table businesses add column if not exists review_summary_approved_at timestamptz;
alter table businesses add column if not exists review_summary_approved_by text;
