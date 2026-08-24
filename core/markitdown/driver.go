package markitdown

// visionDriverScript is executed by the managed venv Python interpreter to run
// markitdown with LLM-assisted image captioning. The markitdown 0.1.4 CLI
// exposes no LLM flags — llm_client/llm_model/llm_prompt are constructor-only
// Python kwargs — so vision-assisted conversion must go through the library
// API instead of the plain CLI.
//
// The script is a fixed constant: connection parameters arrive exclusively
// via the MARKITDOWN_LLM_* environment variables set by visionEnv (keeping the
// API key out of argv), and the only positional argument is the file path.
//
// The embedded client is a minimal stdlib-only implementation of the OpenAI
// SDK surface markitdown consumes: chat.completions.create(model, messages)
// returning an object shaped like choices[0].message.content. The venv does
// not ship the `openai` package (markitdown[all] does not pull it in), so
// urllib.request is used directly; it validates TLS certificates by default
// and honors http_proxy/https_proxy environment variables, which the app sets
// globally when a proxy is configured (see core/proxy SetEnvVars).
//
// Failure semantics: markitdown's own converters catch per-image captioning
// errors (e.g. an unreachable endpoint) and degrade to metadata-only output,
// so a broken vision configuration yields a converted document without
// captions rather than a failed conversion.
const visionDriverScript = `import json, os, sys, urllib.request

_api_key = os.environ["MARKITDOWN_LLM_API_KEY"]
_base_url = os.environ["MARKITDOWN_LLM_BASE_URL"].rstrip("/")
_model = os.environ["MARKITDOWN_LLM_MODEL"]
_prompt = os.environ.get("MARKITDOWN_LLM_PROMPT")


class _Obj:
    """Minimal attribute bag mirroring the OpenAI SDK response shape."""


class _Completions:
    def create(self, model=None, messages=None):
        payload = json.dumps({"model": model, "messages": messages}).encode("utf-8")
        req = urllib.request.Request(
            _base_url + "/chat/completions",
            data=payload,
            method="POST",
            headers={
                "Content-Type": "application/json",
                "Authorization": "Bearer " + _api_key,
            },
        )
        with urllib.request.urlopen(req, timeout=600) as resp:
            data = json.loads(resp.read().decode("utf-8"))
        # Pass content through verbatim (including None): markitdown treats a
        # None caption as "no description" and skips it.
        msg = _Obj()
        msg.content = data["choices"][0]["message"]["content"]
        choice = _Obj()
        choice.message = msg
        result = _Obj()
        result.choices = [choice]
        return result


class _Chat:
    def __init__(self):
        self.completions = _Completions()


class _Client:
    def __init__(self):
        self.chat = _Chat()


from markitdown import MarkItDown

_md = MarkItDown(llm_client=_Client(), llm_model=_model, llm_prompt=_prompt)
sys.stdout.write(_md.convert(sys.argv[1]).markdown)
`

// visionEnvName* are the environment variables the driver script reads.
// Namespaced with the MARKITDOWN_ prefix to avoid colliding with generic
// OPENAI_API_KEY semantics the child might otherwise inherit.
const (
	visionEnvAPIKey  = "MARKITDOWN_LLM_API_KEY"
	visionEnvBaseURL = "MARKITDOWN_LLM_BASE_URL"
	visionEnvModel   = "MARKITDOWN_LLM_MODEL"
	visionEnvPrompt  = "MARKITDOWN_LLM_PROMPT"
)
