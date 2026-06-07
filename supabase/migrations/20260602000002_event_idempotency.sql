-- Navy Lily — webhook idempotency.
-- AbacatePay retries webhooks, so the same event can arrive more than once. The
-- Go server checks payment_events before acting, but this unique index makes the
-- guarantee airtight even under concurrent deliveries: a duplicate insert of
-- the same (charge_id, event_type, charge_status) triple is rejected by Postgres.
--
-- Partial index: only real charge events (non-empty charge_id) are deduped;
-- charge_id-less noise events are still logged freely.
create unique index if not exists uniq_payment_event
  on public.payment_events (charge_id, event_type, charge_status)
  where charge_id <> '';
