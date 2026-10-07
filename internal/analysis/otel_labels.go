package analysis

import (
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/trustabl/trustabl/internal/analysis/astutil"
	"github.com/trustabl/trustabl/internal/models"
)

// otelLabelsPyModule and otelLabelsTSModule are the import names of the
// agent-reliability-otel-labels bindings the pass inspects.
const (
	otelLabelsPyModule = "agent_reliability_otel_labels"
	otelLabelsTSModule = "@trustabl/agent-reliability-otel-labels"
)

// otelLabelsCallee maps the last segment of a callee to the rung it proves.
// NeedsReceiver is set for a method: Run.mark_start only ever appears as
// `<run>.mark_start(...)`, so a receiver-less call of that name is some other
// function.
type otelLabelsCallee struct {
	Name          string
	Kind          models.OtelLabelsSignalKind
	NeedsReceiver bool
}

var otelLabelsPyCallees = []otelLabelsCallee{
	{"mark_start", models.OtelLabelsRunStart, true},
	{"mark_tool_span", models.OtelLabelsToolSpan, false},
	{"mark_run_end", models.OtelLabelsRunEnd, false},
}

var otelLabelsTSCallees = []otelLabelsCallee{
	{"markStart", models.OtelLabelsRunStart, true},
	{"markToolSpan", models.OtelLabelsToolSpan, false},
	{"markRunEnd", models.OtelLabelsRunEnd, false},
}

// DiscoverOtelLabels extracts every agent-reliability-otel-labels signal from
// the parsed files: where the library is imported, where a run is started,
// where a tool span is labelled, and where a run's end is recorded.
//
// The call names are plausible names for a project's own helpers, so they
// count only when the library is imported somewhere in the repo. The gate is
// repo-wide rather than per-file because a Run is routinely built in one module
// and handed to another, which then calls run.mark_start(root) without
// importing anything.
//
// Like DiscoverObservability, this inspects Python and TypeScript/JavaScript
// only. The Go binding is not inspected.
func DiscoverOtelLabels(parsed []ParsedFile) []models.OtelLabelsSignal {
	var out []models.OtelLabelsSignal
	for _, pf := range parsed {
		lang, ok := otelLabelsLanguage(pf)
		if !ok || !importsOtelLabels(pf, lang) {
			continue
		}
		out = append(out, models.OtelLabelsSignal{
			Kind:      models.OtelLabelsImport,
			File:      pf.RelPath,
			StartLine: 1,
			EndLine:   1,
			Language:  lang,
		})
	}
	if len(out) == 0 {
		return nil
	}
	for _, pf := range parsed {
		lang, ok := otelLabelsLanguage(pf)
		if !ok {
			continue
		}
		out = append(out, otelLabelsCallSignals(pf, lang)...)
	}
	sort.Slice(out, func(i, j int) bool {
		switch {
		case out[i].File != out[j].File:
			return out[i].File < out[j].File
		case out[i].StartLine != out[j].StartLine:
			return out[i].StartLine < out[j].StartLine
		default:
			return out[i].Kind < out[j].Kind
		}
	})
	return out
}

// otelLabelsLanguage reports the language a parsed file is inspected as.
// Dispatches on extension explicitly, for the reason DiscoverObservability
// does: the parsed set also carries Go/C#/PHP/Rust trees.
func otelLabelsLanguage(pf ParsedFile) (models.Language, bool) {
	switch {
	case pf.Tree == nil:
		return "", false
	case astutil.ParserKindForExtension(pf.RelPath) != "":
		return models.LanguageTypeScript, true
	case strings.HasSuffix(pf.RelPath, ".py"):
		return models.LanguagePython, true
	}
	return "", false
}

func importsOtelLabels(pf ParsedFile, lang models.Language) bool {
	if lang == models.LanguagePython {
		return fileImportsModule(pf, func(mod string) bool {
			return mod == otelLabelsPyModule || strings.HasPrefix(mod, otelLabelsPyModule+".")
		})
	}
	aliases := astutil.TSImportAliasesMatch(pf.Tree.RootNode(), pf.Source, func(mod string) bool {
		return mod == otelLabelsTSModule || strings.HasPrefix(mod, otelLabelsTSModule+"/")
	})
	return len(aliases) > 0
}

// otelLabelsCallSignals collects the run-start, tool-span and run-end calls
// in one file.
func otelLabelsCallSignals(pf ParsedFile, lang models.Language) []models.OtelLabelsSignal {
	callType, callees := "call", otelLabelsPyCallees
	if lang == models.LanguageTypeScript {
		callType, callees = "call_expression", otelLabelsTSCallees
	}
	var out []models.OtelLabelsSignal
	astutil.Walk(pf.Tree.RootNode(), func(n *sitter.Node) bool {
		if n.Type() != callType {
			return true
		}
		fn := n.ChildByFieldName("function")
		if fn == nil {
			return true
		}
		callee := strings.TrimSpace(astutil.NodeText(fn, pf.Source))
		name, hasReceiver := callee, false
		if i := strings.LastIndex(callee, "."); i >= 0 {
			name, hasReceiver = callee[i+1:], true
		}
		for _, c := range callees {
			if name != c.Name || (c.NeedsReceiver && !hasReceiver) {
				continue
			}
			out = append(out, models.OtelLabelsSignal{
				Kind:      c.Kind,
				File:      pf.RelPath,
				StartLine: astutil.NodeLine(n),
				EndLine:   astutil.NodeEndLine(n),
				Language:  lang,
			})
		}
		return true
	})
	return out
}
