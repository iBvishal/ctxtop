// Package metrics writes a snapshot in the Prometheus text exposition
// format, for ctxtop serve. Metric names are a public interface, like the
// snapshot JSON: adding one is fine, renaming or removing one is a breaking
// change.
package metrics

import (
	"io"
	"strconv"
	"strings"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// ContentType is the media type of what Write produces.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

type label struct{ name, value string }

type sample struct {
	labels []label
	value  float64
}

// family is one metric: its samples must be written together, after a
// single HELP and TYPE line.
type family struct {
	name, help string
	samples    []sample
}

func (f *family) add(v float64, labels ...label) {
	f.samples = append(f.samples, sample{labels, v})
}

// Write writes every metric in s. All metrics are gauges: they describe the
// state at s.Time. Metrics the runtime doesn't report are left out.
func Write(w io.Writer, s snapshot.Snapshot, version string) error {
	var (
		build     = &family{name: "ctxtop_build_info", help: "ctxtop's version; always 1."}
		pollTime  = &family{name: "ctxtop_last_poll_timestamp_seconds", help: "When the servers and host were last read, as a Unix time."}
		memTotal  = &family{name: "ctxtop_host_memory_total_bytes", help: "Physical memory."}
		memUsed   = &family{name: "ctxtop_host_memory_used_bytes", help: "Physical memory in use."}
		swapTotal = &family{name: "ctxtop_host_swap_total_bytes", help: "Swap size."}
		swapUsed  = &family{name: "ctxtop_host_swap_used_bytes", help: "Swap in use."}
		pressure  = &family{name: "ctxtop_host_memory_pressure", help: "Memory pressure as the OS reports it: 0 normal, 1 warn, 2 critical."}
		gpuUtil   = &family{name: "ctxtop_host_gpu_utilization_ratio", help: "Fraction of time the GPU was busy."}
		gpuMem    = &family{name: "ctxtop_host_gpu_memory_used_bytes", help: "GPU memory in use."}
		power     = &family{name: "ctxtop_host_power_watts", help: "Average power since the previous read."}

		srvUp   = &family{name: "ctxtop_server_up", help: "1 when the server was read, 0 when it was found but couldn't be read."}
		srvInfo = &family{name: "ctxtop_server_info", help: "A discovered server; always 1."}

		ctxSize  = &family{name: "ctxtop_model_context_size_tokens", help: "The model's context size."}
		mem      = &family{name: "ctxtop_model_memory_bytes", help: "Memory the model uses."}
		memGPU   = &family{name: "ctxtop_model_memory_gpu_bytes", help: "The part of the model's memory in GPU memory."}
		memCPU   = &family{name: "ctxtop_model_memory_cpu_bytes", help: "The part of the model's memory that spilled to CPU RAM."}
		queued   = &family{name: "ctxtop_model_queued_requests", help: "Requests waiting for a free slot."}
		parallel = &family{name: "ctxtop_model_parallel_slots", help: "Requests the model serves at once."}
		unload   = &family{name: "ctxtop_model_unload_timestamp_seconds", help: "When the runtime will unload the idle model, as a Unix time."}

		slotBusy  = &family{name: "ctxtop_slot_busy", help: "1 while the slot is processing a request."}
		slotSize  = &family{name: "ctxtop_slot_context_size_tokens", help: "The slot's share of the context."}
		slotUsed  = &family{name: "ctxtop_slot_context_used_tokens", help: "Tokens in the slot's context."}
		slotFill  = &family{name: "ctxtop_slot_context_fill_ratio", help: "Fraction of the slot's context in use."}
		slotGen   = &family{name: "ctxtop_slot_generation_tokens_per_second", help: "Generation speed between the last two polls."}
		slotPromp = &family{name: "ctxtop_slot_prompt_tokens_per_second", help: "Prompt processing speed between the last two polls."}

		adv = &family{name: "ctxtop_advice", help: "Advice that applies now; always 1. See ctxtop doctor for the message and fix."}
	)

	build.add(1, label{"version", version})
	if !s.Time.IsZero() {
		pollTime.add(float64(s.Time.UnixMilli()) / 1000)
	}

	h := s.Host
	if h.MemTotal > 0 {
		memTotal.add(float64(h.MemTotal))
		memUsed.add(float64(h.MemUsed))
	}
	if h.SwapTotal > 0 {
		swapTotal.add(float64(h.SwapTotal))
		swapUsed.add(float64(h.SwapUsed))
	}
	switch h.Pressure {
	case "normal":
		pressure.add(0)
	case "warn":
		pressure.add(1)
	case "critical":
		pressure.add(2)
	}
	if h.GPUUtil != nil {
		gpuUtil.add(*h.GPUUtil / 100)
		gpuMem.add(float64(h.GPUMem))
	}
	if p := h.Power; p != nil {
		power.add(p.CPU, label{"component", "cpu"})
		power.add(p.GPU, label{"component", "gpu"})
		power.add(p.ANE, label{"component", "ane"})
	}

	for _, srv := range s.Servers {
		sl := []label{{"runtime", srv.Runtime}, {"url", srv.URL}}
		up := 1.0
		if srv.Error != "" && len(srv.Models) == 0 {
			up = 0
		}
		srvUp.add(up, sl...)
		srvInfo.add(1, append(sl, label{"build", srv.Build}, label{"process", srv.Process})...)

		for _, m := range srv.Models {
			ml := append(sl[:2:2], label{"model", m.Name})
			if m.ContextSize > 0 {
				ctxSize.add(float64(m.ContextSize), ml...)
			}
			if m.Mem > 0 {
				mem.add(float64(m.Mem), ml...)
				if m.MemGPU != nil {
					memGPU.add(float64(*m.MemGPU), ml...)
					memCPU.add(float64(m.Mem-min(*m.MemGPU, m.Mem)), ml...)
				}
			}
			if m.Queued != nil {
				queued.add(float64(*m.Queued), ml...)
			}
			if m.Parallel > 0 {
				parallel.add(float64(m.Parallel), ml...)
			}
			if m.UnloadsAt != nil {
				unload.add(float64(m.UnloadsAt.Unix()), ml...)
			}
			for _, slot := range m.Slots {
				l := append(ml[:3:3], label{"slot", strconv.Itoa(slot.ID)})
				busy := 0.0
				if slot.Busy {
					busy = 1
				}
				slotBusy.add(busy, l...)
				slotSize.add(float64(slot.ContextSize), l...)
				slotUsed.add(float64(slot.ContextUsed), l...)
				slotFill.add(slot.ContextFill, l...)
				slotGen.add(slot.GenTPS, l...)
				slotPromp.add(slot.PromptTPS, l...)
			}
		}
	}

	for _, a := range s.Advice {
		adv.add(1, label{"rule", a.Rule}, label{"level", a.Level}, label{"subject", a.Subject})
	}

	var b strings.Builder
	for _, f := range []*family{
		build, pollTime,
		memTotal, memUsed, swapTotal, swapUsed, pressure, gpuUtil, gpuMem, power,
		srvUp, srvInfo,
		ctxSize, mem, memGPU, memCPU, queued, parallel, unload,
		slotBusy, slotSize, slotUsed, slotFill, slotGen, slotPromp,
		adv,
	} {
		writeFamily(&b, f)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeFamily(w *strings.Builder, f *family) {
	if len(f.samples) == 0 {
		return
	}
	w.WriteString("# HELP " + f.name + " " + f.help + "\n")
	w.WriteString("# TYPE " + f.name + " gauge\n")
	for _, s := range f.samples {
		w.WriteString(f.name)
		if len(s.labels) > 0 {
			w.WriteByte('{')
			for i, l := range s.labels {
				if i > 0 {
					w.WriteByte(',')
				}
				w.WriteString(l.name + `="` + escape(l.value) + `"`)
			}
			w.WriteByte('}')
		}
		w.WriteByte(' ')
		w.WriteString(strconv.FormatFloat(s.value, 'f', -1, 64))
		w.WriteByte('\n')
	}
}

var escaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escape(s string) string { return escaper.Replace(s) }
