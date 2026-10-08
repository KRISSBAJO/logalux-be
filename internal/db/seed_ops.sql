-- Sample payouts for development, so the payouts screen has every state to show.
insert into payouts (business_id, amount_cents, currency, status, provider, reference, failure_reason, scheduled_for, paid_at)
select b.id, p.amount, b.currency, p.status, p.provider, p.ref, p.fail, current_date + p.day_offset,
       case when p.status = 'paid' then now() + (p.day_offset || ' days')::interval end
from businesses b join (values
  ('ada',        341280, 'paid',      'stripe',   'po_1Qa9fK2', '',                                   -1),
  ('ada',        218040, 'paid',      'stripe',   'po_1Qa8dL7', '',                                   -2),
  ('ada',        486050, 'scheduled', 'stripe',   '',           '',                                    1),
  ('barberloft', 302200, 'scheduled', 'stripe',   '',           '',                                    1),
  ('glow',       104800, 'paid',      'stripe',   'po_1Qa7cM3', '',                                   -1),
  ('nia',         98400, 'held',      'stripe',   '',           'Trust case: off-platform deposits',   0),
  ('mnm',     184000000, 'paid',      'paystack', 'TRF_k2m81x', '',                                   -1),
  ('mnm',      96000000, 'scheduled', 'paystack', '',           '',                                    1),
  ('freedomway', 29000000, 'failed',  'paystack', 'TRF_9xq204', 'Account name mismatch at the bank',  -1),
  ('freedomway', 12500000, 'failed',  'paystack', 'TRF_9xq377', 'Account name mismatch at the bank',   0)
) as p(slug, amount, status, provider, ref, fail, day_offset) on p.slug = b.slug;

update businesses set payout_hold = true, payout_hold_reason = 'Trust case: off-platform deposits' where slug = 'nia';
