package jupyter

type Kernel interface {
	Info() KernelInfo
	Shutdown(restart bool) error
}

type KernelInfo struct {
	// Version of messaging protocol.
	// The first integer indicates major version.  It is incremented when
	// there is any backward incompatible change.
	// The second integer indicates minor version.  It is incremented when
	// there is any backward compatible change.
	//
	// Note to implementers: the ProtocolVersion value returned by
	// Kernel.Info() will be ignored, and will be replaced by the
	// igo package.
	ProtocolVersion string `json:"protocol_version"`

	// The kernel implementation name
	// (e.g. 'ipython' for the IPython kernel)
	Implementation string `json:"implementation"`

	// Implementation version number.
	// The version number of the kernel's implementation
	// (e.g. IPython.__version__ for the IPython kernel)
	ImplementationVersion string `json:"implementation_version"`

	// Information about the language of code for the kernel
	LanguageInfo LanguageInfo `json:"language_info"`

	// A banner of information about the kernel,
	// which may be desplayed in console environments.
	Banner string `json:"banner,omitempty"`

	// A list of dictionaries, each with keys 'text' and 'url'.
	// These will be displayed in the help menu in the notebook UI.
	HelpLinks []HelpLink `json:"help_links,omitempty"`
}

type LanguageInfo struct {
	// Name of the programming language in which kernel is implemented.
	// Kernel included in IPython returns 'python'.
	Name string `json:"name"`

	// Language version number.
	// It is Python version number (e.g., '2.7.3') for the kernel
	// included in IPython.
	Version string `json:"version"`

	// mimetype for script files in this language
	Mimetype string `json:"mimetype"`

	// Extension including the dot, e.g. '.py'
	FileExtension string `json:"file_extension"`

	// Pygments lexer, for highlighting
	// Only needed if it differs from the 'name' field.
	PygmentsLexer string `json:"pygments_lexer,omitempty"`

	// Codemirror mode, for for highlighting in the notebook.
	// Only needed if it differs from the 'name' field.
	CodemirrorMode interface{} `json:"codemirror_mode,omitempty"`

	// Nbconvert exporter, if notebooks written with this kernel should
	// be exported with something other than the general 'script'
	// exporter.
	NbconvertExporter string `json:nbconvert_exporter,omitempty"`
}

type HelpLink struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}
