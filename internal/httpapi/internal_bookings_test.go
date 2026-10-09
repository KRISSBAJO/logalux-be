package httpapi

import (
	"context"
	"testing"
)

func TestSecurityInternalReviewRules(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	biz, _, _, user, bk := securityFixture(t, s)
	mid := securityID(t, s, `insert into merchant_users(email,name,password_hash) values('self-review@example.test','QA','unused') returning id::text`)
	_, err := s.pool.Exec(ctx, `insert into merchant_customer_links(merchant_id,user_id) values($1,$2)`, mid, user)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `insert into merchant_members(merchant_id,business_id,role) values($1,$2,'staff')`, mid, biz)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `insert into reviews(business_id,booking_id,author_name,service_name,rating,body) values($1,$2,'QA','QA',5,'A sufficiently long review')`, biz, bk)
	if !isCode(err, "23514") {
		t.Fatalf("staff self review not blocked: %v", err)
	}
	_, err = s.pool.Exec(ctx, `delete from merchant_members where merchant_id=$1`, mid)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `update bookings set is_internal=true where id=$1`, bk)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `insert into reviews(business_id,booking_id,author_name,service_name,rating,body) values($1,$2,'QA','QA',5,'A sufficiently long review')`, biz, bk)
	if !isCode(err, "23514") {
		t.Fatalf("internal review not blocked: %v", err)
	}
	_, err = s.pool.Exec(ctx, `update bookings set is_internal=false where id=$1`, bk)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `insert into reviews(business_id,booking_id,author_name,service_name,rating,body) values($1,$2,'QA','QA',5,'A sufficiently long review')`, biz, bk)
	if err != nil {
		t.Fatalf("unaffiliated review blocked: %v", err)
	}
}
