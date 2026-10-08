-- Payouts created before the ledger existed had no ledger line, so a business
-- could show money as both "paid out" and "available". Give each of those
-- payouts its line. A paid one takes its amount off the balance when that
-- leaves the balance at zero or more. A scheduled one becomes "the available
-- balance, on its way", which is what a scheduled payout means.
do $$
declare p record; avail int;
begin
  for p in
    select po.id, po.business_id, po.amount_cents, po.currency, po.status, po.provider, coalesce(po.paid_at, po.created_at) as at
    from payouts po
    where po.status in ('paid', 'scheduled')
      and not exists (select 1 from ledger l where l.payout_id = po.id)
      and exists (select 1 from ledger l where l.business_id = po.business_id)
    order by (po.status = 'scheduled'), po.created_at
  loop
    select coalesce(sum(amount_cents), 0) into avail from ledger where business_id = p.business_id and in_balance and status = 'settled';
    if p.status = 'paid' then
      continue when avail < p.amount_cents;
      insert into ledger (business_id, kind, amount_cents, currency, method, status, payout_id, description, created_at)
      values (p.business_id, 'payout', -p.amount_cents, p.currency, p.provider, 'settled', p.id, 'Payout to bank', p.at);
    else
      continue when avail < 100;
      update payouts set amount_cents = avail where id = p.id;
      insert into ledger (business_id, kind, amount_cents, currency, method, status, payout_id, description)
      values (p.business_id, 'payout', -avail, p.currency, p.provider, 'settled', p.id, 'Payout to bank');
    end if;
  end loop;
end $$;
