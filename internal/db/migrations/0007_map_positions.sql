-- The sample businesses all sat on their city's centre point. Give each its
-- own neighbourhood position so the map is truthful. Real businesses get
-- their position from their address when it is saved.

update locations l set lat = v.lat, lng = v.lng
from businesses b, (values
  ('ada',        36.1936, -86.7446),  -- Gallatin Ave, East Nashville
  ('barberloft', 36.1513, -86.7868),  -- Division St, The Gulch
  ('glow',       36.1247, -86.7897),  -- 12th Ave S, 12 South
  ('nia',        36.0598, -86.6722),  -- Antioch
  ('freedomway', 6.4474,  3.4723),    -- Freedom Way, Lekki
  ('mnm',        6.4478,  3.4701),    -- Admiralty Way, Lekki Phase 1
  ('glowup',     6.4391,  3.4850),    -- Lekki
  ('tiwa',       6.4541,  3.4316)     -- Bourdillon Rd, Ikoyi
) as v(slug, lat, lng)
where b.id = l.business_id and b.slug = v.slug and l.is_primary;

create index locations_position on locations (lat, lng) where lat is not null;
