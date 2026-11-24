// Package mock provides deterministic local text processing for demonstrations and tests.
package mock

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var ErrUnavailable = errors.New("mock LLM service unavailable")

const NotFoundAnswer = "Ответ в материалах не найден."

var sentenceSeparator = regexp.MustCompile(`[.!?]+\s*`)

// Client implements deterministic extraction, not neural text generation.
type Client struct{}

// New creates a mock LLM client.
func New() *Client { return &Client{} }

// Summarize returns a prepared summary for the release fixture or extracts two sentences.
func (c *Client) Summarize(ctx context.Context, transcript string) (string, error) {
	if err := waitScenario(ctx, transcript); err != nil {
		return "", err
	}
	clean := strings.TrimSpace(transcript)
	if strings.Contains(strings.ToLower(clean), "завершить тестирование до пятницы") {
		return "Команда согласовала завершение тестирования до пятницы. Алексей отвечает за тестирование, Мария — за документацию.", nil
	}
	sentences := splitSentences(clean)
	if len(sentences) == 0 {
		return "", errors.New("transcript is empty")
	}
	if len(sentences) > 2 {
		sentences = sentences[:2]
	}
	return strings.Join(sentences, ". ") + ".", nil
}

// Answer returns the context sentence with the greatest meaningful word overlap.
func (c *Client) Answer(ctx context.Context, contextText, question string) (string, error) {
	if err := waitScenario(ctx, contextText); err != nil {
		return "", err
	}
	terms := meaningfulWords(question)
	if len(terms) == 0 {
		return NotFoundAnswer, nil
	}
	type candidate struct {
		text  string
		score int
	}
	var candidates []candidate
	for _, sentence := range splitSentences(contextText) {
		words := meaningfulWords(sentence)
		score := 0
		for term := range terms {
			if _, ok := words[term]; ok {
				score++
			}
		}
		if score > 0 {
			candidates = append(candidates, candidate{text: sentence, score: score})
		}
	}
	if len(candidates) == 0 {
		return NotFoundAnswer, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	return candidates[0].text + ".", nil
}

func waitScenario(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch {
	case strings.Contains(text, "[llm:error]"):
		return ErrUnavailable
	case strings.Contains(text, "[llm:timeout]"):
		<-ctx.Done()
		return ctx.Err()
	default:
		return nil
	}
}

func splitSentences(text string) []string {
	parts := sentenceSeparator.Split(text, -1)
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" && !strings.HasPrefix(part, "[") {
			result = append(result, part)
		}
	}
	return result
}

func meaningfulWords(text string) map[string]struct{} {
	stop := map[string]bool{"что": true, "кто": true, "как": true, "когда": true, "где": true, "какой": true, "какие": true, "за": true, "по": true, "и": true, "в": true, "на": true, "о": true, "об": true, "для": true, "это": true}
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	result := make(map[string]struct{})
	for _, word := range words {
		if len([]rune(word)) >= 3 && !stop[word] {
			result[word] = struct{}{}
		}
	}
	return result
}
