package postgres

import (
	"alfa.local/shared/model"
	"context"
	"encoding/json"
)

func (s Store) ArticleRevision(ctx context.Context, id, company string, revision int) (model.Article, error) {
	var out model.Article
	var b []byte
	e := s.Pool.QueryRow(ctx, `SELECT r.document FROM news.article_revisions r JOIN news.articles a ON a.id=r.article_id WHERE a.id=$1 AND r.revision=$2 AND (a.scope='public' OR a.scope=$3)`, id, revision, "company:"+company).Scan(&b)
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(b, &out)
	return out, e
}
