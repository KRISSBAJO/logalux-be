-- Each hero photo can carry its own short quote, and say where the quote card
-- sits so it never covers the subject of that photo.

alter table site_media
  add column caption text not null default '',
  add column caption_pos text not null default 'bottom-right'
    check (caption_pos in ('bottom-right','bottom-left','top-right','top-left','none'));
