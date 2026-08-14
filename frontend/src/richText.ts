/**
 * 助手的回答本身是纯文本，但开启联网搜索之后，模型会用 Markdown 写引用：
 * `([reddit.com](https://www.reddit.com/…))`。这里只认这一种写法，其余字符
 * 原样保留——回答不是 Markdown 文档，把整套语法引进来只会让转写里出现的
 * `*`、`_` 变成格式。
 *
 * 片段交给模板渲染，链接文字和正文都走 Vue 的插值，所以模型（以及它读到的
 * 网页）写什么都变不成 HTML。链接地址只允许 http/https：其余协议留在原文里
 * 当普通文字，免得把 `javascript:`、`file:` 交给系统去打开。
 */
export type AnswerSegment =
  | { kind: 'text'; text: string }
  | { kind: 'link'; text: string; url: string }

// URL 部分不接受空白和括号：Markdown 允许成对括号，但引用里几乎不会出现，
// 而放宽之后 `([站点](地址))` 外层的右括号就会被当成地址的一部分。
const MARKDOWN_LINK = /\[([^\]\n]*)\]\(([^()\s]+)\)/g

/** externalURL 返回可以交给系统浏览器打开的地址，不能打开时返回空串。 */
export function externalURL(raw: string): string {
  try {
    const parsed = new URL(raw)
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.href : ''
  } catch {
    return ''
  }
}

export function answerSegments(answer: string): AnswerSegment[] {
  const segments: AnswerSegment[] = []
  let consumed = 0
  for (const match of answer.matchAll(MARKDOWN_LINK)) {
    const start = match.index ?? 0
    const url = externalURL(match[2])
    // 认不出的地址整段留作原文，用户至少还看得到模型给的是什么。
    if (!url) continue
    if (start > consumed) segments.push({ kind: 'text', text: answer.slice(consumed, start) })
    segments.push({ kind: 'link', text: match[1].trim() || hostOf(url), url })
    consumed = start + match[0].length
  }
  if (consumed < answer.length) segments.push({ kind: 'text', text: answer.slice(consumed) })
  return segments
}

function hostOf(url: string): string {
  try {
    return new URL(url).hostname
  } catch {
    return url
  }
}
