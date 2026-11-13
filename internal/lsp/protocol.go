package lsp

// The part of the Language Server Protocol 3.17 the server speaks. Field
// names and numbers are the specification's.

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"` // UTF-16 code units
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type InitializeParams struct {
	RootURI               string             `json:"rootUri"`
	InitializationOptions *InitOptions       `json:"initializationOptions"`
	Capabilities          ClientCapabilities `json:"capabilities"`
}

// ClientCapabilities is the little the server reads of what the client can do.
type ClientCapabilities struct {
	Workspace struct {
		DidChangeWatchedFiles struct {
			DynamicRegistration bool `json:"dynamicRegistration"`
		} `json:"didChangeWatchedFiles"`
	} `json:"workspace"`
	TextDocument struct {
		Completion struct {
			CompletionItem struct {
				SnippetSupport bool `json:"snippetSupport"`
			} `json:"completionItem"`
		} `json:"completion"`
	} `json:"textDocument"`
}

// InitOptions are Phaenon's own initialization options.
type InitOptions struct {
	// Features is "diagnostics" for a client that answers everything else
	// itself, PhpStorm through its PSI (design/tooling.md §6.2): the server
	// then advertises diagnostics and their quick-fixes only. Anything else
	// is the whole of §6.1's list.
	Features string `json:"features"`
	// BuildOnSave writes the outputs after each save, as phn build does.
	BuildOnSave *bool `json:"buildOnSave"`
}

type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier `json:"textDocument"`
	ContentChanges []TextDocumentContentChange     `json:"contentChanges"`
}

// TextDocumentContentChange is a whole text, or with Range a replacement.
type TextDocumentContentChange struct {
	Range *Range `json:"range,omitempty"`
	Text  string `json:"text"`
}

type DidSaveTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type DidChangeConfigurationParams struct {
	Settings struct {
		Phaenon *InitOptions `json:"phaenon"`
	} `json:"settings"`
}

type DidChangeWatchedFilesParams struct {
	Changes []FileEvent `json:"changes"`
}

type FileEvent struct {
	URI  string `json:"uri"`
	Type int    `json:"type"`
}

// ShowMessageParams is window/showMessage, a message the client shows the
// user.
type ShowMessageParams struct {
	Type    int    `json:"type"` // 1 error, 2 warning, 3 info, 4 log
	Message string `json:"message"`
}

type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type Diagnostic struct {
	Range              Range                          `json:"range"`
	Severity           int                            `json:"severity"` // 1 error, 2 warning
	Code               string                         `json:"code"`
	Source             string                         `json:"source"`
	Message            string                         `json:"message"`
	RelatedInformation []DiagnosticRelatedInformation `json:"relatedInformation,omitempty"`
}

type DiagnosticRelatedInformation struct {
	Location Location `json:"location"`
	Message  string   `json:"message"`
}

type MarkupContent struct {
	Kind  string `json:"kind"` // "markdown"
	Value string `json:"value"`
}

type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// Symbol kinds.
const (
	symbolNamespace   = 3
	symbolClass       = 5
	symbolMethod      = 6
	symbolProperty    = 7
	symbolConstructor = 9
	symbolEnum        = 10
	symbolInterface   = 11
	symbolFunction    = 12
	symbolConstant    = 14
	symbolEnumMember  = 22
	symbolStruct      = 23 // a trait: LSP has no kind of its own for one
)

type CompletionParams struct {
	TextDocumentPositionParams
}

type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

type CompletionItem struct {
	Label               string     `json:"label"`
	Kind                int        `json:"kind,omitempty"`
	Detail              string     `json:"detail,omitempty"`
	Documentation       string     `json:"documentation,omitempty"`
	SortText            string     `json:"sortText,omitempty"`
	FilterText          string     `json:"filterText,omitempty"`
	InsertText          string     `json:"insertText,omitempty"`
	InsertTextFormat    int        `json:"insertTextFormat,omitempty"` // 1 plain, 2 snippet
	TextEdit            *TextEdit  `json:"textEdit,omitempty"`
	AdditionalTextEdits []TextEdit `json:"additionalTextEdits,omitempty"`
}

// Completion item kinds.
const (
	completionMethod    = 2
	completionFunction  = 3
	completionField     = 5
	completionVariable  = 6
	completionClass     = 7
	completionInterface = 8
	completionConstant  = 21
	completionEnum      = 13
	completionEnumCase  = 20
	completionKeyword   = 14
)

type SignatureHelp struct {
	Signatures      []SignatureInformation `json:"signatures"`
	ActiveSignature int                    `json:"activeSignature"`
	ActiveParameter int                    `json:"activeParameter"`
}

type SignatureInformation struct {
	Label         string                 `json:"label"`
	Documentation string                 `json:"documentation,omitempty"`
	Parameters    []ParameterInformation `json:"parameters"`
}

type ParameterInformation struct {
	Label [2]int `json:"label"` // the parameter's offsets in the signature's label, UTF-16
}

type DocumentFormattingParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

type CodeActionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      struct {
		Diagnostics []Diagnostic `json:"diagnostics"`
	} `json:"context"`
}

type CodeAction struct {
	Title       string         `json:"title"`
	Kind        string         `json:"kind"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
	IsPreferred bool           `json:"isPreferred,omitempty"`
	Edit        *WorkspaceEdit `json:"edit,omitempty"`
}

type WorkspaceEdit struct {
	Changes map[string][]TextEdit `json:"changes"`
}

// BuildParams is the server's own notification `phaenon/build`, sent after
// each build on save, which the PhpStorm plugin shows in its status bar.
type BuildParams struct {
	Files      int    `json:"files"`
	Written    int    `json:"written"`
	Withheld   int    `json:"withheld"` // files with errors, not compiled (D10)
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}
