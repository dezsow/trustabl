ADK passes each tool a context that carries its `execute_tool` span, so
`trace.SpanFromContext(tc)` reaches it. The run labels go on ADK's own
`invoke_agent` span through agent callbacks; no application root span is
needed.

ADK builds the agent, its tools and its callbacks once, so none of them can
hold the `Run`: every run would share it. Put the run state on the context
passed to `runner.Run` instead. ADK hands that context's values to every tool
and callback of the run.

```go
type runState struct {
	run      *otellabels.Run
	mu       sync.Mutex
	attempts map[string]int
}

type runStateKey struct{}

// withRunState starts a run's state. Call it once per runner.Run.
func withRunState(ctx context.Context) context.Context {
	return context.WithValue(ctx, runStateKey{},
		&runState{run: otellabels.NewRun(""), attempts: map[string]int{}})
}

func runStateFrom(ctx context.Context) *runState {
	st, _ := ctx.Value(runStateKey{}).(*runState)
	return st
}

// Built once, at startup.
func newTravelBooker(llm model.LLM) (agent.Agent, error) {
	searchFlights, err := functiontool.New(functiontool.Config{
		Name:        "search_flights",
		Description: "Search flights between two airports on a date.",
	}, func(tc agent.ToolContext, a searchArgs) (result searchResult, err error) {
		st := runStateFrom(tc)
		if st == nil { // runner.Run was not given withRunState
			return findFlights(a)
		}
		rawArgs, _ := json.Marshal(a)
		key, fpErr := otellabels.CanonicalFP(json.RawMessage(rawArgs))
		if fpErr != nil {
			key = "search_flights (no fingerprint)"
		}
		st.mu.Lock()
		st.attempts[key]++
		attempt := st.attempts[key]
		st.mu.Unlock()
		step := st.run.NextStep()
		span := trace.SpanFromContext(tc)

		defer func() {
			call := otellabels.ToolCall{
				Args: json.RawMessage(rawArgs), Attempt: attempt,
				SideEffect: otellabels.Read, StepIndex: &step,
				RunID: st.run.RunID(), Name: "search_flights",
			}
			if err == nil {
				rawResult, _ := json.Marshal(result)
				call.Result = json.RawMessage(rawResult)
			}
			if mErr := otellabels.MarkToolSpan(span, call); mErr != nil {
				fmt.Fprintln(os.Stderr, "trustabl: could not label the tool span:", mErr)
			}
		}()
		return findFlights(a)
	})
	if err != nil {
		return nil, err
	}

	return llmagent.New(llmagent.Config{
		Name:        "travel_booker",
		Description: "Books flights.",
		Model:       llm,
		Tools:       []tool.Tool{searchFlights},
		BeforeAgentCallbacks: []agent.BeforeAgentCallback{func(cc agent.CallbackContext) (*genai.Content, error) {
			if st := runStateFrom(cc); st != nil {
				st.run.MarkStart(trace.SpanFromContext(cc))
			}
			return nil, nil
		}},
		AfterAgentCallbacks: []agent.AfterAgentCallback{func(cc agent.CallbackContext) (*genai.Content, error) {
			if runStateFrom(cc) == nil {
				return nil, nil
			}
			if mErr := otellabels.MarkRunEnd(trace.SpanFromContext(cc), otellabels.FinalAnswer); mErr != nil {
				fmt.Fprintln(os.Stderr, "trustabl: could not mark the run's end:", mErr)
			}
			return nil, nil
		}},
	})
}

// One call per run: each gets its own run state.
func ask(ctx context.Context, r *runner.Runner, userID, sessionID string, msg *genai.Content) error {
	for _, err := range r.Run(withRunState(ctx), userID, sessionID, msg, agent.RunConfig{}) {
		if err != nil {
			return err
		}
	}
	return nil
}
```

- ADK runs a turn's tool calls in parallel goroutines; the mutex guards the
  attempt counts, and `NextStep` is already safe for concurrent use.
- With several calls in a turn, ADK adds an `execute_tool (merged)` span
  around them. It carries no labels; skip it by span name.
- ADK skips the after-agent callback when the caller stops iterating the
  run's events, so a run that stops on its first failure carries no
  `trustabl.exit_reason`.
