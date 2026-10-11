-- A rotating access token still belongs to the same device session. Keep live
-- browser handoffs attached when it changes; logout must still revoke them.
alter table customer_web_handoffs
 drop constraint customer_web_handoffs_source_session_hash_fkey,
 add constraint customer_web_handoffs_source_session_hash_fkey
 foreign key (source_session_hash) references user_sessions(token_hash)
 on update cascade on delete cascade;
