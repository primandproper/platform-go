package elasticsearch

import "encoding/json"

type multiMatchQuery struct {
	Query  string   `json:"query"`
	Type   string   `json:"type"`
	Fields []string `json:"fields"`
}

type queryContainer struct {
	MultiMatch multiMatchQuery `json:"multi_match"`
}

type searchQuery struct {
	Query queryContainer `json:"query"`
}

type esHit struct {
	ID         string          `json:"_id"`
	Source     json.RawMessage `json:"_source"`
	Highlights json.RawMessage `json:"highlight"`
	Sort       []any           `json:"sort"`
}

type esResponse struct {
	Hits struct {
		Hits  []*esHit
		Total struct{ Value int }
	}
}
