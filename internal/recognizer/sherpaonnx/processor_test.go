package sherpaonnx

import "testing"

func TestResultProcessorPartialFinalAndEndpointBehavior(t *testing.T) {
	processor := newResultProcessor(80)

	update := processor.update("你", false)
	if !update.emitPartial || update.partial != "你" || update.emitFinal || update.reset {
		t.Fatalf("first partial = %#v", update)
	}
	update = processor.update("你", false)
	if update.emitPartial || update.emitFinal || update.reset {
		t.Fatalf("duplicate partial was not suppressed: %#v", update)
	}
	update = processor.update("你好", true)
	if !update.emitPartial || update.partial != "你好" || !update.emitFinal || update.final != "你好" || !update.reset {
		t.Fatalf("endpoint update = %#v", update)
	}
	update = processor.update("", true)
	if update.emitPartial || update.emitFinal || !update.reset {
		t.Fatalf("empty endpoint update = %#v", update)
	}
}

func TestResultProcessorCountsUnicodeCharactersForForcedFinal(t *testing.T) {
	processor := newResultProcessor(3)
	update := processor.update("你好吗", false)
	if !update.emitFinal || update.final != "你好吗" || !update.reset {
		t.Fatalf("forced final = %#v", update)
	}
}

func TestResultProcessorFinishFinalizesUnchangedPartial(t *testing.T) {
	processor := newResultProcessor(80)
	_ = processor.update("尾巴", false)
	update := processor.finish("")
	if update.emitPartial || !update.emitFinal || update.final != "尾巴" || update.reset {
		t.Fatalf("finish update = %#v", update)
	}
}
