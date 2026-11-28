package app

import (
	"strings"
	"testing"
	"unicode/utf8"
)

var benchmarkContextResult string

func BenchmarkBoundedContext(b *testing.B) {
	tests := []struct {
		name  string
		parts []string
	}{
		{
			name:  "small-russian",
			parts: []string{"Обсудили план релиза. Алексей проверит сборку, Мария обновит документацию."},
		},
		{
			name: "near-limit-mixed",
			parts: []string{
				"Обсуждение release plan: команда согласовала сроки и ответственных. " + strings.Repeat("Алексей проверит интеграцию, Мария подготовит release notes. ", 200),
			},
		},
		{
			name:  "large-truncated",
			parts: []string{strings.Repeat("Обсудили релиз, тестирование, сроки и ответственных. ", 700)},
		},
	}

	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			parts := sequence(test.parts)
			result := boundedContext(parts, 12000)
			if !utf8.ValidString(result) || utf8.RuneCountInString(result) > 12000 {
				b.Fatal("bounded context is invalid UTF-8 or exceeds the rune limit")
			}
			if test.name == "near-limit-mixed" && utf8.RuneCountInString(result) != 12000 {
				b.Fatal("near-limit fixture did not exercise the full rune limit")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkContextResult = boundedContext(parts, 12000)
			}
		})
	}
}

func BenchmarkBoundedContextSeqEarlyStop(b *testing.B) {
	fragment := strings.Repeat("Обсудили sprint release, тестирование и сроки. ", 20)
	parts := make([]string, 2000)
	for i := range parts {
		parts[i] = fragment
	}
	sequence := sequence(parts)
	result := boundedContext(sequence, 12000)
	if !utf8.ValidString(result) || utf8.RuneCountInString(result) != 12000 {
		b.Fatal("bounded context is invalid UTF-8 or did not reach the rune limit")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchmarkContextResult = boundedContext(sequence, 12000)
	}
}
