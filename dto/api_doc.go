package dto

type ApiDoc struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Content     string `json:"content"`
	Filename    string `json:"filename,omitempty"`
}
