package calls

import (
	"context"
	"fmt"
	"time"

	"concierge/engine/internal/verify"
)

// CheckInstructions are the voice model's standing orders on a check call: it says only what each
// turn asks, and never judges an answer — calld does (answeredAloud).
const CheckInstructions = "You are the concierge's phone line check. Speak only when asked, and say only what you are asked to say, " +
	"in the language you are asked to use, briefly and naturally. Never comment on the caller's answers yourself."

// speaks is each language's name as the model is told it.
var speaks = map[string]string{"en": "English", "pt": "European Portuguese", "ru": "Russian", "es": "Spanish"}

// phrases are the check's four sentences, written in one language. The model gets them already
// translated and is told to say them word for word: handed an English quote to "say only" in
// Russian, it translated the question but read the goodbye out in English (2026-10-28).
type phrases struct{ ask, again, thanks, goodbye string } // ask and again take A and B

var checkPhrases = map[string]phrases{
	"en": {
		ask:     "This is your concierge, checking the line. What is %d plus %d?",
		again:   "Sorry, that's not it. What is %d plus %d?",
		thanks:  "That's right, thank you. Goodbye.",
		goodbye: "Sorry, that's not right. Goodbye.",
	},
	"pt": {
		ask:     "Fala o seu concierge, a verificar a linha. Quanto é %d mais %d?",
		again:   "Desculpe, não é isso. Quanto é %d mais %d?",
		thanks:  "Está certo, obrigado. Adeus.",
		goodbye: "Desculpe, não está certo. Adeus.",
	},
	"ru": {
		ask:     "Это ваш консьерж, проверяю линию. Сколько будет %d плюс %d?",
		again:   "Извините, это не так. Сколько будет %d плюс %d?",
		thanks:  "Верно, спасибо. До свидания.",
		goodbye: "Извините, это неверно. До свидания.",
	},
	"es": {
		ask:     "Soy su conserje, compruebo la línea. ¿Cuánto es %d más %d?",
		again:   "Perdone, no es eso. ¿Cuánto es %d más %d?",
		thanks:  "Correcto, gracias. Adiós.",
		goodbye: "Lo siento, no es correcto. Adiós.",
	},
}

// sayExactly is one turn's instruction: the sentence, already in the language, said word for word.
func sayExactly(language, sentence string) string {
	return fmt.Sprintf(`Say exactly these words in %s, nothing else and nothing translated back: "%s"`, language, sentence)
}

// answerWait is how long a turn waits for the owner's answer once the question has been asked.
const answerWait = 15 * time.Second

// spokenCheck is the Voice setup check as a conversation: the model asks "What is A plus B?" in
// lang; calld judges the transcript of each answer; one wrong or silent answer gets the question
// once more, a second ends it. passed, wrong, or failed when the call or the session ends first. Its
// summary is deterministic, from what calld knows: the sum, the language, the last answer heard and
// the outcome — no model call.
func spokenCheck(ch verify.Challenge, lang Language, wait time.Duration) Script {
	in, p := speaks[lang.Code], checkPhrases[lang.Code]
	ask := sayExactly(in, fmt.Sprintf(p.ask, ch.A, ch.B))
	again := sayExactly(in, fmt.Sprintf(p.again, ch.A, ch.B))
	thanks := sayExactly(in, p.thanks)
	goodbye := sayExactly(in, p.goodbye)
	return func(ctx context.Context, t Talker) (string, string) {
		last := "" // the last answer heard
		summary := func(outcome string) (string, string) {
			heard := "nothing"
			if last != "" {
				heard = fmt.Sprintf(`"%s"`, last) // as transcribed, unescaped
			}
			return outcome, fmt.Sprintf("Asked %d + %d in %s; heard %s; %s.", ch.A, ch.B, in, heard, outcome)
		}
		say := func(instructions string) bool {
			if err := t.Respond(ctx, instructions); err != nil {
				return false
			}
			select {
			case <-t.Spoken():
				return true
			case <-t.Done():
			case <-ctx.Done():
			}
			return false
		}
		// nextAnswer is the next answer ("" after wait in silence); ok is false when the call ended.
		nextAnswer := func() (answer string, ok bool) {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case answer = <-t.Heard():
				return answer, true
			case <-timer.C:
				return "", true
			case <-t.Done():
			case <-ctx.Done():
			}
			return "", false
		}
		if !say(ask) {
			return summary("failed")
		}
		for try := 0; try < 2; try++ {
			answer, ok := nextAnswer()
			if !ok {
				return summary("failed")
			}
			if answer != "" {
				last = answer
				if answeredAloud(ch, lang.Code, answer) {
					say(thanks)
					return summary("passed")
				}
			}
			if try == 0 && !say(again) {
				return summary("failed")
			}
		}
		say(goodbye)
		return summary("wrong")
	}
}
