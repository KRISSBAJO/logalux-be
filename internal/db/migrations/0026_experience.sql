-- Booking for someone else, repeat appointments, questions asked at booking, and calendar sync.

alter table bookings
  add column if not exists guest_name text not null default '',   -- who is coming, when it is not the person who booked
  add column if not exists series_id uuid;                        -- bookings made together as a repeat
create index if not exists bookings_series on bookings (series_id) where series_id is not null;

-- Questions a business asks when someone books: about every booking, or about one service.
create table if not exists intake_questions (
  id          uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  service_id  uuid references services(id) on delete cascade,     -- null: asked for every service
  label       text not null,
  kind        text not null check (kind in ('text', 'yesno', 'choice', 'consent')),
  options     text[] not null default '{}',                       -- for kind = choice
  required    boolean not null default false,
  sort        int not null default 0,
  active      boolean not null default true,
  created_at  timestamptz not null default now()
);
create index if not exists intake_questions_business on intake_questions (business_id, sort);

-- What the client answered. The question's wording is kept with the answer, so later edits do not rewrite history.
create table if not exists booking_answers (
  booking_id  uuid not null references bookings(id) on delete cascade,
  question_id uuid references intake_questions(id) on delete set null,
  label       text not null,
  kind        text not null,
  answer      text not null,
  sort        int not null default 0
);
create index if not exists booking_answers_booking on booking_answers (booking_id);

-- Calendar sync, per person. Out: a private address other calendars subscribe to. In: the private
-- address of the person's own calendar, read every ten minutes so their busy times cannot be booked.
alter table staff
  add column if not exists cal_token text,
  add column if not exists cal_import_url text,
  add column if not exists cal_import_at timestamptz,
  add column if not exists cal_import_note text not null default '';
create unique index if not exists staff_cal_token on staff (cal_token) where cal_token is not null;

alter table calendar_blocks add column if not exists external boolean not null default false;
create index if not exists calendar_blocks_external on calendar_blocks (staff_id) where external;
