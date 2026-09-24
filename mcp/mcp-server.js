#!/usr/bin/env node
/**
 * DocShare MCP Server（零依赖，Node >= 18，stdio 传输）
 *
 * 把 DocShare 开放平台 API（/openapi/v1，HMAC-SHA256 签名）包装成 MCP 工具，
 * 供 CodeBuddy / Claude / Cursor 等 AI 客户端直接创建、更新、检索文档。
 *
 * 密钥配置（二选一，环境变量优先）：
 *   1. 环境变量：DOC_SHARE_BASE_URL / DOC_SHARE_APP_KEY / DOC_SHARE_SECRET
 *   2. 配置文件：mcp/mcp.config.json（参考 mcp/mcp.config.example.json，已被 .gitignore 忽略）
 *
 * 客户端接入示例（CodeBuddy / Claude Desktop 的 mcpServers 配置）：
 *   {
 *     "mcpServers": {
 *       "docshare": {
 *         "command": "node",
 *         "args": ["D:/code/go/doc-share/mcp/mcp-server.js"],
 *         "env": { "DOC_SHARE_BASE_URL": "http://your-server:9000",
 *                  "DOC_SHARE_APP_KEY": "ak_xxx", "DOC_SHARE_SECRET": "sk_xxx" }
 *       }
 *     }
 *   }
 *
 * 密钥在 DocShare 后台 /admin/apikeys 创建（Secret 仅创建时显示一次）。
 * 密钥属主即文档所有者：viewer 的密钥只能操作自己的资源，与后台权限一致。
 */
'use strict';

const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

/* ---------- 配置：环境变量优先，其次脚本同目录 mcp.config.json ---------- */

function loadConfig() {
  let file = {};
  try {
    file = JSON.parse(fs.readFileSync(path.join(__dirname, 'mcp.config.json'), 'utf8'));
  } catch (_) { /* 配置文件可选 */ }
  return {
    baseUrl: String(process.env.DOC_SHARE_BASE_URL || file.baseUrl || 'http://127.0.0.1:9000').replace(/\/+$/, ''),
    appKey: String(process.env.DOC_SHARE_APP_KEY || file.appKey || ''),
    secret: String(process.env.DOC_SHARE_SECRET || file.secret || ''),
  };
}

const cfg = loadConfig();

/* ---------- HMAC 签名请求（与服务端 stringToSign 严格对齐） ----------
 * 待签串 = appKey \n METHOD \n path(不含 query) \n timestamp \n nonce \n hex(sha256(body))
 * 签名   = hex( HMAC-SHA256(secret, 待签串) )
 */
function clean(obj) {
  const out = {};
  for (const [k, v] of Object.entries(obj || {})) {
    if (v !== undefined && v !== null && v !== '') out[k] = v;
  }
  return out;
}

async function apiCall(method, pathname, query, body) {
  if (!cfg.appKey || !cfg.secret) {
    throw new Error('未配置密钥：请设置环境变量 DOC_SHARE_APP_KEY / DOC_SHARE_SECRET，或在 mcp/ 目录创建 mcp.config.json（参考 mcp/mcp.config.example.json）');
  }
  let url = cfg.baseUrl + pathname;
  const qs = new URLSearchParams(clean(query)).toString();
  if (qs) url += '?' + qs;

  const bodyStr = body ? JSON.stringify(body) : '';
  const ts = String(Math.floor(Date.now() / 1000));
  const nonce = crypto.randomBytes(16).toString('hex'); // 32 位，满足服务端 8~64 位要求
  const bodyHash = crypto.createHash('sha256').update(bodyStr, 'utf8').digest('hex');
  const sts = [cfg.appKey, method, pathname, ts, nonce, bodyHash].join('\n');
  const sig = crypto.createHmac('sha256', cfg.secret).update(sts, 'utf8').digest('hex');

  const res = await fetch(url, {
    method,
    headers: {
      'Content-Type': 'application/json',
      'X-App-Key': cfg.appKey,
      'X-Timestamp': ts,
      'X-Nonce': nonce,
      'X-Signature': sig,
    },
    body: method === 'GET' || method === 'DELETE' ? undefined : bodyStr,
  });
  const text = await res.text();
  if (!res.ok) {
    throw new Error(`HTTP ${res.status}: ${text.slice(0, 500)}`);
  }
  try {
    return JSON.parse(text);
  } catch (_) {
    return text;
  }
}

/* ---------- 工具定义 ---------- */

const num = { type: 'number', description: '数字 ID' };
const docSchema = {
  type: 'object',
  properties: {
    id: { ...num, description: '文档 ID' },
    title: { type: 'string', description: '文档标题（必填；更新时留空表示不修改）' },
    content: { type: 'string', description: 'Markdown 正文；更新时总是整体覆盖' },
    project_id: { type: 'number', description: '所属项目 ID（0=未分组；只能归属到密钥属主自己的项目）' },
    category_id: { type: 'number', description: '所属分类 ID（0=未分类；同上）' },
  },
  required: ['id'],
};

const TOOLS = [
  {
    name: 'docshare_list_docs',
    description: '列出 DocShare 文档（分页，不含正文），可按项目/分类过滤',
    schema: {
      type: 'object',
      properties: {
        page: { type: 'number', description: '页码，从 1 开始，默认 1' },
        size: { type: 'number', description: '每页条数，1~100，默认 20' },
        project_id: { type: 'number', description: '按项目 ID 过滤' },
        category_id: { type: 'number', description: '按分类 ID 过滤' },
      },
    },
    async run(args) {
      return apiCall('GET', '/openapi/v1/docs', args);
    },
  },
  {
    name: 'docshare_get_doc',
    description: '读取单个文档的完整内容（含 Markdown 正文）',
    schema: docSchema,
    async run(args) {
      return apiCall('GET', `/openapi/v1/docs/${Number(args.id)}`);
    },
  },
  {
    name: 'docshare_create_doc',
    description: '创建 Markdown 文档',
    schema: {
      type: 'object',
      properties: {
        title: { type: 'string', description: '文档标题（必填）' },
        content: { type: 'string', description: 'Markdown 正文' },
        project_id: { type: 'number', description: '所属项目 ID（可选，0=未分组）' },
        category_id: { type: 'number', description: '所属分类 ID（可选，0=未分类）' },
      },
      required: ['title'],
    },
    async run(args) {
      return apiCall('POST', '/openapi/v1/docs', null, clean({
        title: args.title, content: args.content || '',
        project_id: args.project_id, category_id: args.category_id,
      }));
    },
  },
  {
    name: 'docshare_update_doc',
    description: '更新文档：content 总是整体覆盖；title 留空表示不修改',
    schema: docSchema,
    async run(args) {
      return apiCall('PUT', `/openapi/v1/docs/${Number(args.id)}`, null, clean({
        title: args.title, content: args.content || '',
        project_id: args.project_id, category_id: args.category_id,
      }));
    },
  },
  {
    name: 'docshare_delete_doc',
    description: '删除文档（不可恢复）',
    schema: docSchema,
    async run(args) {
      return apiCall('DELETE', `/openapi/v1/docs/${Number(args.id)}`);
    },
  },
  {
    name: 'docshare_list_projects',
    description: '列出密钥属主的项目',
    schema: { type: 'object', properties: {} },
    async run() {
      return apiCall('GET', '/openapi/v1/projects');
    },
  },
  {
    name: 'docshare_create_project',
    description: '创建项目',
    schema: {
      type: 'object',
      properties: {
        name: { type: 'string', description: '项目名称（必填，≤100 字）' },
        description: { type: 'string', description: '描述（可选，≤500 字）' },
      },
      required: ['name'],
    },
    async run(args) {
      return apiCall('POST', '/openapi/v1/projects', null, clean(args));
    },
  },
  {
    name: 'docshare_update_project',
    description: '更新项目名称/描述',
    schema: {
      type: 'object',
      properties: {
        id: num,
        name: { type: 'string', description: '项目名称' },
        description: { type: 'string', description: '描述' },
      },
      required: ['id', 'name'],
    },
    async run(args) {
      return apiCall('PUT', `/openapi/v1/projects/${Number(args.id)}`, null, clean({
        name: args.name, description: args.description,
      }));
    },
  },
  {
    name: 'docshare_delete_project',
    description: '删除项目（其下文档回到未分组，不级联删除）',
    schema: { type: 'object', properties: { id: num }, required: ['id'] },
    async run(args) {
      return apiCall('DELETE', `/openapi/v1/projects/${Number(args.id)}`);
    },
  },
  {
    name: 'docshare_list_categories',
    description: '列出密钥属主的分类',
    schema: { type: 'object', properties: {} },
    async run() {
      return apiCall('GET', '/openapi/v1/categories');
    },
  },
  {
    name: 'docshare_create_category',
    description: '创建分类',
    schema: {
      type: 'object',
      properties: {
        name: { type: 'string', description: '分类名称（必填，≤50 字）' },
        sort: { type: 'number', description: '排序值（可选）' },
      },
      required: ['name'],
    },
    async run(args) {
      return apiCall('POST', '/openapi/v1/categories', null, clean(args));
    },
  },
  {
    name: 'docshare_update_category',
    description: '更新分类名称/排序',
    schema: {
      type: 'object',
      properties: {
        id: num,
        name: { type: 'string', description: '分类名称' },
        sort: { type: 'number', description: '排序值' },
      },
      required: ['id', 'name'],
    },
    async run(args) {
      return apiCall('PUT', `/openapi/v1/categories/${Number(args.id)}`, null, clean(args));
    },
  },
  {
    name: 'docshare_delete_category',
    description: '删除分类（其下文档变为未分类）',
    schema: { type: 'object', properties: { id: num }, required: ['id'] },
    async run(args) {
      return apiCall('DELETE', `/openapi/v1/categories/${Number(args.id)}`);
    },
  },
];

/* ---------- MCP stdio（JSON-RPC 2.0，按行分帧） ---------- */

function send(obj) {
  process.stdout.write(JSON.stringify(obj) + '\n');
}

function reply(id, result) {
  send({ jsonrpc: '2.0', id, result });
}

function replyErr(id, code, message) {
  send({ jsonrpc: '2.0', id, error: { code, message } });
}

function handleLine(line) {
  let msg;
  try {
    msg = JSON.parse(line);
  } catch (_) {
    return; // 忽略无法解析的行
  }
  const { id, method, params } = msg;

  if (method === 'initialize') {
    // 协商协议版本：客户端请求的版本受支持则回显，否则回落到广泛兼容的 2024-11-05
    const known = ['2024-11-05', '2025-03-26', '2025-06-18'];
    const requested = params && params.protocolVersion;
    const ver = known.includes(requested) ? requested : '2024-11-05';
    reply(id, {
      protocolVersion: ver,
      capabilities: { tools: { listChanged: false } },
      serverInfo: { name: 'docshare-mcp', version: '1.0.0' },
    });
    return;
  }
  if (id === undefined || id === null) {
    return; // 通知（notifications/initialized 等）：不回包
  }
  switch (method) {
    case 'ping':
      reply(id, {});
      return;
    case 'tools/list':
      reply(id, {
        tools: TOOLS.map((t) => ({ name: t.name, description: t.description, inputSchema: t.schema })),
      });
      return;
    case 'tools/call':
      handleToolCall(id, params || {});
      return;
    default:
      replyErr(id, -32601, 'Method not found: ' + method);
  }
}

async function handleToolCall(id, params) {
  const tool = TOOLS.find((t) => t.name === params.name);
  if (!tool) {
    reply(id, { content: [{ type: 'text', text: '未知工具: ' + params.name }], isError: true });
    return;
  }
  try {
    const out = await tool.run(params.arguments || {});
    reply(id, {
      content: [{ type: 'text', text: typeof out === 'string' ? out : JSON.stringify(out, null, 2) }],
    });
  } catch (e) {
    reply(id, {
      content: [{ type: 'text', text: '调用失败: ' + ((e && e.message) || e) }],
      isError: true,
    });
  }
}

process.stdin.setEncoding('utf8');
let buf = '';
process.stdin.on('data', (chunk) => {
  buf += chunk;
  let idx;
  while ((idx = buf.indexOf('\n')) >= 0) {
    const line = buf.slice(0, idx).trim();
    buf = buf.slice(idx + 1);
    if (line) handleLine(line);
  }
});
process.stdin.on('end', () => process.exit(0));

process.stderr.write(`[docshare-mcp] 已启动 baseUrl=${cfg.baseUrl} appKey=${cfg.appKey ? cfg.appKey[0] + '***' : '(未配置)'}\n`);
