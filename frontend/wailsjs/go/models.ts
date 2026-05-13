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
	
	export class ConfigProviderFull {
	    base_url: string;
	    api_key: string;
	    model: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigProviderFull(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.base_url = source["base_url"];
	        this.api_key = source["api_key"];
	        this.model = source["model"];
	    }
	}
	export class ConfigProviderKeyModel {
	    api_key: string;
	    model: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigProviderKeyModel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.api_key = source["api_key"];
	        this.model = source["model"];
	    }
	}
	export class ConfigLLMResponse {
	    active_provider: string;
	    anthropic: ConfigProviderKeyModel;
	    gemini: ConfigProviderKeyModel;
	    lmstudio: ConfigProviderFull;
	    openai_compatible: ConfigProviderFull;
	    chatgpt: ConfigProviderKeyModel;
	
	    static createFrom(source: any = {}) {
	        return new ConfigLLMResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active_provider = source["active_provider"];
	        this.anthropic = this.convertValues(source["anthropic"], ConfigProviderKeyModel);
	        this.gemini = this.convertValues(source["gemini"], ConfigProviderKeyModel);
	        this.lmstudio = this.convertValues(source["lmstudio"], ConfigProviderFull);
	        this.openai_compatible = this.convertValues(source["openai_compatible"], ConfigProviderFull);
	        this.chatgpt = this.convertValues(source["chatgpt"], ConfigProviderKeyModel);
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
	export class ConfigMemResponse {
	    database: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigMemResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.database = source["database"];
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
	    memory: ConfigMemResponse;
	    search: ConfigSearchResp;
	
	    static createFrom(source: any = {}) {
	        return new ConfigResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.loaded = source["loaded"];
	        this.log_level = source["log_level"];
	        this.config_errors = source["config_errors"];
	        this.llm = this.convertValues(source["llm"], ConfigLLMResponse);
	        this.memory = this.convertValues(source["memory"], ConfigMemResponse);
	        this.search = this.convertValues(source["search"], ConfigSearchResp);
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
	export class LLMSettingsRequest {
	    active_provider: string;
	    api_key: string;
	    base_url: string;
	    model: string;
	
	    static createFrom(source: any = {}) {
	        return new LLMSettingsRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active_provider = source["active_provider"];
	        this.api_key = source["api_key"];
	        this.base_url = source["base_url"];
	        this.model = source["model"];
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
	
	    static createFrom(source: any = {}) {
	        return new SecuritySettingsResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.default_policy = source["default_policy"];
	        this.tool_policies = this.convertValues(source["tool_policies"], ToolPolicyResponse, true);
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

