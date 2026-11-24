package mock

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	text := "Обсудили релиз. Решили завершить тестирование до пятницы. Алексей отвечает за тестирование. Мария подготовит документацию."
	summary, err := New().Summarize(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	want := "Команда согласовала завершение тестирования до пятницы. Алексей отвечает за тестирование, Мария — за документацию."
	if summary != want {
		t.Fatalf("summary = %q", summary)
	}
}

func TestAnswer(t *testing.T) {
	client := New()
	contextText := "Релиз состоится в понедельник. Алексей отвечает за тестирование. Мария готовит документацию."
	answer, err := client.Answer(context.Background(), contextText, "Кто отвечает за тестирование?")
	if err != nil || answer != "Алексей отвечает за тестирование." {
		t.Fatalf("Answer = %q, %v", answer, err)
	}
	answer, err = client.Answer(context.Background(), contextText, "Какой бюджет согласовали?")
	if err != nil || answer != NotFoundAnswer {
		t.Fatalf("missing Answer = %q, %v", answer, err)
	}
}

func TestErrorsAndCancellation(t *testing.T) {
	client := New()
	if _, err := client.Summarize(context.Background(), "[llm:error]"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := client.Summarize(ctx, "[llm:timeout]"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout = %v", err)
	}
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := client.Answer(canceled, "text", "question"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled = %v", err)
	}
}
