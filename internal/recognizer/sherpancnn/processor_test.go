package sherpancnn

import "testing"

func TestResultProcessorPartialEndpointAndReset(t *testing.T) {
	processor := newResultProcessor(80)
	first := processor.update("你", false)
	if !first.emitPartial || first.partial != "你" || first.emitFinal || first.reset {
		t.Fatalf("first update = %#v", first)
	}
	duplicate := processor.update("你", false)
	if duplicate.emitPartial || duplicate.emitFinal || duplicate.reset {
		t.Fatalf("duplicate update = %#v", duplicate)
	}
	endpoint := processor.update("你好", true)
	if !endpoint.emitPartial || !endpoint.emitFinal || endpoint.final != "你好" || !endpoint.reset {
		t.Fatalf("endpoint update = %#v", endpoint)
	}
	emptyEndpoint := processor.update("", true)
	if emptyEndpoint.emitPartial || emptyEndpoint.emitFinal || !emptyEndpoint.reset {
		t.Fatalf("empty endpoint = %#v", emptyEndpoint)
	}
}

func TestResultProcessorForcedRuneBoundaryAndFlush(t *testing.T) {
	processor := newResultProcessor(3)
	update := processor.update("中文句", false)
	if !update.emitFinal || update.final != "中文句" || !update.reset {
		t.Fatalf("forced boundary = %#v", update)
	}
	processor.update("尾巴", false)
	finish := processor.finish("")
	if !finish.emitFinal || finish.final != "尾巴" {
		t.Fatalf("flush = %#v", finish)
	}
}
