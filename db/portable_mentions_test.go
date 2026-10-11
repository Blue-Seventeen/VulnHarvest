package db

import (
	"context"
	"fmt"
	"testing"
)

func TestPortableMentionsContracts(t *testing.T) {
	task2Stores(t, func(t *testing.T, d *DB) {
		ctx := context.Background()
		var exactID int64
		for i := 0; i < 25; i++ {
			var id int64
			if err := d.QueryRow(`INSERT INTO companies(nkey,name) VALUES($1,$2) RETURNING id`, fmt.Sprintf("task2-%d", i), fmt.Sprintf("MixedCase Fixture %d", i)).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if i == 3 {
				exactID = id
			}
		}
		if _, err := d.Exec(`INSERT INTO companies(nkey,name) VALUES('unicode','Écho')`); err != nil {
			t.Fatal(err)
		}
		unicode, err := d.SearchChatMentionsPage(ctx, "company", "écho", "")
		if err != nil || len(unicode.Items) != 1 {
			t.Fatalf("Unicode ILIKE equivalence: %+v %v", unicode, err)
		}
		first, err := d.SearchChatMentionsPage(ctx, "company", "mixedcase", "")
		if err != nil || len(first.Items) != 20 || first.NextCursor == "" {
			t.Fatalf("page one: %+v %v", first, err)
		}
		second, err := d.SearchChatMentionsPage(ctx, "company", "mixedcase", first.NextCursor)
		if err != nil || len(second.Items) != 5 || second.NextCursor != "" {
			t.Fatalf("page two: %+v %v", second, err)
		}
		seen := map[int64]bool{}
		for _, item := range append(first.Items, second.Items...) {
			if seen[item.ID] {
				t.Fatalf("repeated id %d", item.ID)
			}
			seen[item.ID] = true
		}
		exact, err := d.SearchChatMentionsPage(ctx, "company", fmt.Sprint(exactID), "")
		if err != nil || len(exact.Items) == 0 || exact.Items[0].ID != exactID {
			t.Fatalf("exact priority: %+v %v", exact, err)
		}
		var literalID int64
		if err = d.QueryRow(`INSERT INTO companies(nkey,name) VALUES('task2-percent','Literal%_Back\Slash') RETURNING id`).Scan(&literalID); err != nil {
			t.Fatal(err)
		}
		literal, err := d.SearchChatMentionsPage(ctx, "company", `%_Back\`, "")
		if err != nil || len(literal.Items) != 1 || literal.Items[0].ID != literalID {
			t.Fatalf("literal wildcards: %+v %v", literal, err)
		}
		if _, err = d.SearchChatMentionsPage(ctx, "company", "different", first.NextCursor); err != ErrInvalidChatMentionCursor {
			t.Fatalf("cursor scope: %v", err)
		}
		if _, err = d.Exec(`INSERT INTO assets(type,domain) VALUES('subdomain','MixedCase.example')`); err != nil {
			t.Fatal(err)
		}
		if _, err = d.Exec(`INSERT INTO findings(vulnclass,name) VALUES('fixture','MixedCase finding')`); err != nil {
			t.Fatal(err)
		}
		all, err := d.SearchChatMentionsPage(ctx, "", "mixedcase", "")
		if err != nil || len(all.Items) != 20 {
			t.Fatalf("union: %+v %v", all, err)
		}
	})
}
