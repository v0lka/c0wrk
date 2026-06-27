export namespace backend {
	
	export class BlackboardFactResponse {
	    keywords: string[];
	    content: string;
	    author: string;
	
	    static createFrom(source: any = {}) {
	        return new BlackboardFactResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.keywords = source["keywords"];
	        this.content = source["content"];
	        this.author = source["author"];
	    }
	}
	export class BlackboardPlanStepResponse {
	    id: string;
	    summary: string;
	    description: string;
	    depends_on: string[];
	
	    static createFrom(source: any = {}) {
	        return new BlackboardPlanStepResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.summary = source["summary"];
	        this.description = source["description"];
	        this.depends_on = source["depends_on"];
	    }
	}
	export class BlackboardPlanResponse {
	    steps: BlackboardPlanStepResponse[];
	
	    static createFrom(source: any = {}) {
	        return new BlackboardPlanResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.steps = this.convertValues(source["steps"], BlackboardPlanStepResponse);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class BlackboardReflectionResponse {
	    summary: string;
	    hypotheses?: string[];
	    suggested_action?: string;
	    reasoning?: string;
	    failure_analysis?: string;
	    root_cause?: string;
	    action_plan?: string;
	    timestamp: string;
	
	    static createFrom(source: any = {}) {
	        return new BlackboardReflectionResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.summary = source["summary"];
	        this.hypotheses = source["hypotheses"];
	        this.suggested_action = source["suggested_action"];
	        this.reasoning = source["reasoning"];
	        this.failure_analysis = source["failure_analysis"];
	        this.root_cause = source["root_cause"];
	        this.action_plan = source["action_plan"];
	        this.timestamp = source["timestamp"];
	    }
	}
	export class BlackboardStepResponse {
	    step_id: string;
	    summary: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new BlackboardStepResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.step_id = source["step_id"];
	        this.summary = source["summary"];
	        this.error = source["error"];
	    }
	}
	export class BlackboardStateResponse {
	    task_id: string;
	    session_id: string;
	    status: string;
	    original_request: string;
	    plan?: BlackboardPlanResponse;
	    step_results: Record<string, BlackboardStepResponse>;
	    reflections: BlackboardReflectionResponse[];
	    facts: BlackboardFactResponse[];
	    final_output?: string;
	
	    static createFrom(source: any = {}) {
	        return new BlackboardStateResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.task_id = source["task_id"];
	        this.session_id = source["session_id"];
	        this.status = source["status"];
	        this.original_request = source["original_request"];
	        this.plan = this.convertValues(source["plan"], BlackboardPlanResponse);
	        this.step_results = this.convertValues(source["step_results"], BlackboardStepResponse, true);
	        this.reflections = this.convertValues(source["reflections"], BlackboardReflectionResponse);
	        this.facts = this.convertValues(source["facts"], BlackboardFactResponse);
	        this.final_output = source["final_output"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class ReasoningInfo {
	    options: string[];
	    default: string;
	
	    static createFrom(source: any = {}) {
	        return new ReasoningInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.options = source["options"];
	        this.default = source["default"];
	    }
	}
	export class ModelInfo {
	    name: string;
	    family: string;
	    reasoning?: ReasoningInfo;
	
	    static createFrom(source: any = {}) {
	        return new ModelInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.family = source["family"];
	        this.reasoning = this.convertValues(source["reasoning"], ReasoningInfo);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConfigProviderFull {
	    api_key: string;
	    base_url?: string;
	    models: string[];
	
	    static createFrom(source: any = {}) {
	        return new ConfigProviderFull(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.api_key = source["api_key"];
	        this.base_url = source["base_url"];
	        this.models = source["models"];
	    }
	}
	export class ConfigLLMResponse {
	    default_model: string;
	    anthropic: ConfigProviderFull;
	    openai_compatible: ConfigProviderFull;
	    chatgpt: ConfigProviderFull;
	    all_models: ModelInfo[];
	    models_ready: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ConfigLLMResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.default_model = source["default_model"];
	        this.anthropic = this.convertValues(source["anthropic"], ConfigProviderFull);
	        this.openai_compatible = this.convertValues(source["openai_compatible"], ConfigProviderFull);
	        this.chatgpt = this.convertValues(source["chatgpt"], ConfigProviderFull);
	        this.all_models = this.convertValues(source["all_models"], ModelInfo);
	        this.models_ready = source["models_ready"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class ProxySettingsResponse {
	    enabled: boolean;
	    url: string;
	    bypass_list: string[];
	    tls_cert_dir: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxySettingsResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.url = source["url"];
	        this.bypass_list = source["bypass_list"];
	        this.tls_cert_dir = source["tls_cert_dir"];
	    }
	}
	export class ConfigSearchResp {
	    provider: string;
	    api_key: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigSearchResp(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.api_key = source["api_key"];
	    }
	}
	export class ConfigResponse {
	    loaded: boolean;
	    log_level: string;
	    config_errors: string[];
	    llm: ConfigLLMResponse;
	    search: ConfigSearchResp;
	    proxy: ProxySettingsResponse;
	
	    static createFrom(source: any = {}) {
	        return new ConfigResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.loaded = source["loaded"];
	        this.log_level = source["log_level"];
	        this.config_errors = source["config_errors"];
	        this.llm = this.convertValues(source["llm"], ConfigLLMResponse);
	        this.search = this.convertValues(source["search"], ConfigSearchResp);
	        this.proxy = this.convertValues(source["proxy"], ProxySettingsResponse);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class FileIconResponse {
	    icon: string;
	    icon_color: string;
	
	    static createFrom(source: any = {}) {
	        return new FileIconResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.icon = source["icon"];
	        this.icon_color = source["icon_color"];
	    }
	}
	export class FrontendAPILifecycle {
	
	
	    static createFrom(source: any = {}) {
	        return new FrontendAPILifecycle(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	
	    }
	}
	export class ProviderConfigRequest {
	    api_key?: string;
	    base_url?: string;
	    models?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ProviderConfigRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.api_key = source["api_key"];
	        this.base_url = source["base_url"];
	        this.models = source["models"];
	    }
	}
	export class LLMFullConfigRequest {
	    default_model: string;
	    anthropic?: ProviderConfigRequest;
	    openai_compatible?: ProviderConfigRequest;
	    chatgpt?: ProviderConfigRequest;
	
	    static createFrom(source: any = {}) {
	        return new LLMFullConfigRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.default_model = source["default_model"];
	        this.anthropic = this.convertValues(source["anthropic"], ProviderConfigRequest);
	        this.openai_compatible = this.convertValues(source["openai_compatible"], ProviderConfigRequest);
	        this.chatgpt = this.convertValues(source["chatgpt"], ProviderConfigRequest);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class OptimizePromptResponse {
	    optimized_prompt: string;
	    keywords: string[];
	    used_context: boolean;
	
	    static createFrom(source: any = {}) {
	        return new OptimizePromptResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.optimized_prompt = source["optimized_prompt"];
	        this.keywords = source["keywords"];
	        this.used_context = source["used_context"];
	    }
	}
	export class ProjectUIStateRequest {
	    project_id: string;
	    saved_session_id: string;
	    open_tabs: string[];
	    active_file: string;
	
	    static createFrom(source: any = {}) {
	        return new ProjectUIStateRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.project_id = source["project_id"];
	        this.saved_session_id = source["saved_session_id"];
	        this.open_tabs = source["open_tabs"];
	        this.active_file = source["active_file"];
	    }
	}
	export class ProjectUIStateResponse {
	    project_id: string;
	    saved_session_id: string;
	    open_tabs: string[];
	    active_file: string;
	    updated_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ProjectUIStateResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.project_id = source["project_id"];
	        this.saved_session_id = source["saved_session_id"];
	        this.open_tabs = source["open_tabs"];
	        this.active_file = source["active_file"];
	        this.updated_at = source["updated_at"];
	    }
	}
	
	export class ProxySettingsRequest {
	    enabled: boolean;
	    url: string;
	    bypass_list: string[];
	    tls_cert_dir: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxySettingsRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.url = source["url"];
	        this.bypass_list = source["bypass_list"];
	        this.tls_cert_dir = source["tls_cert_dir"];
	    }
	}
	
	
	export class SearchRequest {
	    query: string;
	    top_k: number;
	    file_pattern: string;
	    must_match: string[];
	    mode: string;
	
	    static createFrom(source: any = {}) {
	        return new SearchRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.query = source["query"];
	        this.top_k = source["top_k"];
	        this.file_pattern = source["file_pattern"];
	        this.must_match = source["must_match"];
	        this.mode = source["mode"];
	    }
	}
	export class SearchSettingsRequest {
	    provider: string;
	    api_key: string;
	
	    static createFrom(source: any = {}) {
	        return new SearchSettingsRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.api_key = source["api_key"];
	    }
	}
	export class ToolPolicyResponse {
	    policy: string;
	    blacklist?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ToolPolicyResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.policy = source["policy"];
	        this.blacklist = source["blacklist"];
	    }
	}
	export class SecuritySettingsResponse {
	    default_policy: string;
	    tool_policies: Record<string, ToolPolicyResponse>;
	    auto_approve_workspace_writes: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SecuritySettingsResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.default_policy = source["default_policy"];
	        this.tool_policies = this.convertValues(source["tool_policies"], ToolPolicyResponse, true);
	        this.auto_approve_workspace_writes = source["auto_approve_workspace_writes"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SessionTokensResponse {
	    total_input_tokens: number;
	    total_output_tokens: number;
	    model: string;
	    family: string;
	
	    static createFrom(source: any = {}) {
	        return new SessionTokensResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total_input_tokens = source["total_input_tokens"];
	        this.total_output_tokens = source["total_output_tokens"];
	        this.model = source["model"];
	        this.family = source["family"];
	    }
	}
	export class SkillDescriptorDTO {
	    name: string;
	    description: string;
	
	    static createFrom(source: any = {}) {
	        return new SkillDescriptorDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	    }
	}
	export class ToolInfo {
	    name: string;
	    description: string;
	    source: string;
	    policy: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	        this.source = source["source"];
	        this.policy = source["policy"];
	    }
	}
	
	export class VectorIndexStatus {
	    state: string;
	    progress: number;
	    files_indexed: number;
	    total_files: number;
	    current_file: string;
	    branch: string;
	    phase: string;
	    indices: string[];
	
	    static createFrom(source: any = {}) {
	        return new VectorIndexStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.progress = source["progress"];
	        this.files_indexed = source["files_indexed"];
	        this.total_files = source["total_files"];
	        this.current_file = source["current_file"];
	        this.branch = source["branch"];
	        this.phase = source["phase"];
	        this.indices = source["indices"];
	    }
	}
	export class VectorStoreEntry {
	    file_path: string;
	    file_name: string;
	    content: string;
	    score: number;
	    start_line: number;
	    end_line: number;
	    language: string;
	    vector_score?: number;
	    lexical_score?: number;
	    vector_rank?: number;
	    lexical_rank?: number;
	
	    static createFrom(source: any = {}) {
	        return new VectorStoreEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.file_path = source["file_path"];
	        this.file_name = source["file_name"];
	        this.content = source["content"];
	        this.score = source["score"];
	        this.start_line = source["start_line"];
	        this.end_line = source["end_line"];
	        this.language = source["language"];
	        this.vector_score = source["vector_score"];
	        this.lexical_score = source["lexical_score"];
	        this.vector_rank = source["vector_rank"];
	        this.lexical_rank = source["lexical_rank"];
	    }
	}

}

export namespace mcp {
	
	export class ServerStatus {
	    name: string;
	    transport: string;
	    connected: boolean;
	    tool_count: number;
	    tools: string[];
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ServerStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.transport = source["transport"];
	        this.connected = source["connected"];
	        this.tool_count = source["tool_count"];
	        this.tools = source["tools"];
	        this.error = source["error"];
	    }
	}

}

export namespace project {
	
	export class ProjectInfo {
	    id: string;
	    name: string;
	    workspace_path: string;
	    is_external: boolean;
	    is_no_project: boolean;
	    created_at: string;
	    last_active_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ProjectInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.workspace_path = source["workspace_path"];
	        this.is_external = source["is_external"];
	        this.is_no_project = source["is_no_project"];
	        this.created_at = source["created_at"];
	        this.last_active_at = source["last_active_at"];
	    }
	}

}

export namespace session {
	
	export class ChatMessage {
	    id: number;
	    session_id: string;
	    role: string;
	    content: string;
	    metadata: number[];
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.session_id = source["session_id"];
	        this.role = source["role"];
	        this.content = source["content"];
	        this.metadata = source["metadata"];
	        this.created_at = source["created_at"];
	    }
	}
	export class SessionInfo {
	    id: string;
	    project_id: string;
	    name: string;
	    created_at: string;
	    last_active_at: string;
	    archived: boolean;
	    active: boolean;
	    total_input_tokens: number;
	    total_output_tokens: number;
	    model: string;
	    family: string;
	    plan_review_phase: string;
	    plan_review_path: string;
	    plan_review_context: string;
	
	    static createFrom(source: any = {}) {
	        return new SessionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.project_id = source["project_id"];
	        this.name = source["name"];
	        this.created_at = source["created_at"];
	        this.last_active_at = source["last_active_at"];
	        this.archived = source["archived"];
	        this.active = source["active"];
	        this.total_input_tokens = source["total_input_tokens"];
	        this.total_output_tokens = source["total_output_tokens"];
	        this.model = source["model"];
	        this.family = source["family"];
	        this.plan_review_phase = source["plan_review_phase"];
	        this.plan_review_path = source["plan_review_path"];
	        this.plan_review_context = source["plan_review_context"];
	    }
	}
	export class TerminalCommand {
	    id: number;
	    session_id: string;
	    command: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new TerminalCommand(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.session_id = source["session_id"];
	        this.command = source["command"];
	        this.created_at = source["created_at"];
	    }
	}

}

export namespace workspace {
	
	export class FileNode {
	    name: string;
	    path: string;
	    is_dir: boolean;
	    icon: string;
	    icon_color: string;
	    hidden: boolean;
	    gitignored: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FileNode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.is_dir = source["is_dir"];
	        this.icon = source["icon"];
	        this.icon_color = source["icon_color"];
	        this.hidden = source["hidden"];
	        this.gitignored = source["gitignored"];
	    }
	}

}

