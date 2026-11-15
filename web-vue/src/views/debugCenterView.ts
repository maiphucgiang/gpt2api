export const DEBUG_SEARCH_API_KEY_PLACEHOLDER = '<YOUR_API_KEY>'

export function debugChatReasoningEffortOptions(model: string) {
  void model
  return [{ label: '默认思考', value: '' }]
}

export function buildDebugSearchSkillMarkdown(apiBaseUrl: string, language: 'zh' | 'en') {
  const endpoint = `${apiBaseUrl.replace(/\/$/, '')}/v1/search`
  if (language === 'en') {
    return `---
name: gpt2api-search
description: Use when current web search is needed through this gpt2api server. Call the configured HTTP search endpoint with a prompt and return the answer with source URLs.
---

# gpt2api Search

Use this skill when the user asks for current web search, online lookup, recent information, or source-backed answers.

## Request

POST ${endpoint}

Headers:

Authorization: Bearer ${DEBUG_SEARCH_API_KEY_PLACEHOLDER}
Content-Type: application/json

JSON body:

{
  "prompt": "<search question>"
}

## Response handling

- Use \`answer\` as the main response.
- Include source URLs from \`sources\` when available.
- If the endpoint returns an error, summarize the error and ask whether to retry.`
  }
  return `---
name: gpt2api-search
description: 当用户需要联网搜索、查询最新信息、核实事实或需要来源链接时，调用本地 gpt2api 搜索接口。
---

# gpt2api 搜索

当用户要求联网搜索、查询最新信息、核实资料、查新闻、查价格、查文档更新或需要来源链接时，使用这个 skill。

## 接口

POST ${endpoint}

Headers:

Authorization: Bearer ${DEBUG_SEARCH_API_KEY_PLACEHOLDER}
Content-Type: application/json

Body:

{
  "prompt": "<用户要搜索的问题>"
}

## 返回处理

- 使用接口返回的 \`answer\` 作为主要回答。
- 如果有 \`sources\`，在回答里附上来源链接。
- 如果接口报错，简要说明错误并询问是否重试。`
}

export function buildDebugSearchSkillInstallPrompt(apiBaseUrl: string, language: 'zh' | 'en') {
  const skill = buildDebugSearchSkillMarkdown(apiBaseUrl, language)
  if (language === 'en') {
    return `Please install a local web-search skill on this machine.

Requirements:
1. Install this as a local skill according to the current environment's skill rules.
2. Skill name: gpt2api-search
3. File name: SKILL.md
4. Only create or update this skill file.

SKILL.md content:

\`\`\`markdown
${skill}
\`\`\``
  }
  return `请帮我在本机安装一个用于联网搜索的 skill。

要求：
1. 按当前环境的 skill 安装规范，把它安装成本地 skill。
2. skill 名称为：gpt2api-search
3. 文件名为：SKILL.md
4. 只创建或更新这个 skill 文件，不要修改其他无关文件。

SKILL.md 内容：

\`\`\`markdown
${skill}
\`\`\``
}
