package mock

import (
	"context"
	"testing"
)

var benchmarkTextResult string

func BenchmarkSummarize(b *testing.B) {
	client := New()
	transcript := "Обсудили релиз. Решили завершить тестирование до пятницы. Алексей отвечает за тестирование. Мария подготовит документацию."
	if _, err := client.Summarize(context.Background(), transcript); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchmarkTextResult, _ = client.Summarize(context.Background(), transcript)
	}
}

func BenchmarkAnswer(b *testing.B) {
	client := New()
	contextText := "Обсудили квартальный релиз и график тестирования. " +
		"Алексей отвечает за интеграционное тестирование до пятницы. " +
		"Мария подготовит release notes и обновит документацию. " +
		"Команда согласовала план запуска на следующую неделю."
	question := "Кто отвечает за тестирование и к какому сроку?"
	if _, err := client.Answer(context.Background(), contextText, question); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchmarkTextResult, _ = client.Answer(context.Background(), contextText, question)
	}
}
