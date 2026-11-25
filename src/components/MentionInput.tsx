import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react'
import { EditorContent, useEditor, type Editor } from '@tiptap/react'
import { Extension } from '@tiptap/core'
import StarterKit from '@tiptap/starter-kit'
import Mention from '@tiptap/extension-mention'
import Placeholder from '@tiptap/extension-placeholder'
import type { SuggestionKeyDownProps, SuggestionProps } from '@tiptap/suggestion'
import { createRoot, type Root } from 'react-dom/client'

export type MentionInputTarget = { id: string; name: string; color: string }

export type MentionInputHandle = { focus: () => void }

type Target = MentionInputTarget

/** doc → 纯文本 + 被 @ 的名字。胶囊序列化成 `@名字`，后端 ParseMentions 按空格分词。 */
function serialize(editor: Editor): { text: string; mentions: string[] } {
  const mentions: string[] = []
  const blocks: string[] = []
  editor.state.doc.forEach((block) => {
    let out = ''
    block.descendants((node) => {
      if (node.type.name === 'mention') {
        const label = String(node.attrs.label ?? node.attrs.id ?? '')
        mentions.push(label)
        out += `@${label}`
      } else if (node.type.name === 'hardBreak') {
        out += '\n'
      } else if (node.isText) {
        out += node.text ?? ''
      }
      return false
    })
    blocks.push(out)
  })
  return { text: blocks.join('\n'), mentions }
}

const escapeHTML = (s: string) =>
  s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')

const escapeReg = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/** 纯文本 → 编辑器 HTML：认识的名字还原成胶囊（回填/重发用），其余按原文。 */
function toHTML(text: string, targets: Target[]): string {
  const names = targets.map((t) => t.name).sort((a, b) => b.length - a.length)
  const byName = new Map(targets.map((t) => [t.name, t]))
  return text
    .split('\n')
    .map((line) => {
      let html = ''
      let rest = line
      while (rest.length > 0) {
        const at = rest.indexOf('@')
        if (at < 0) {
          html += escapeHTML(rest)
          break
        }
        html += escapeHTML(rest.slice(0, at))
        const hit = names.find((n) => rest.startsWith(`@${n}`, at))
        if (hit) {
          const t = byName.get(hit)!
          html += `<span data-type="mention" data-id="${escapeHTML(t.id)}" data-label="${escapeHTML(t.name)}"></span>`
          rest = rest.slice(at + 1 + hit.length)
        } else {
          html += '@'
          rest = rest.slice(at + 1)
        }
      }
      return `<p>${html || '<br>'}</p>`
    })
    .join('')
}

type SuggestState = { items: Target[]; command: (t: Target) => void }

function SuggestList({
  state,
  register,
}: {
  state: SuggestState
  register: (h: (e: KeyboardEvent) => boolean) => void
}) {
  const [idx, setIdx] = useState(0)
  const { items, command } = state

  useEffect(() => setIdx(0), [items])

  useEffect(() => {
    register((e) => {
      if (e.key === 'ArrowDown') {
        setIdx((i) => (i + 1) % Math.max(items.length, 1))
        return true
      }
      if (e.key === 'ArrowUp') {
        setIdx((i) => (i - 1 + items.length) % Math.max(items.length, 1))
        return true
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        if (items.length > 0) command(items[Math.min(idx, items.length - 1)])
        return true
      }
      return false
    })
  }, [items, idx, command, register])

  if (items.length === 0) {
    return <div className="c-mention-suggest__empty">没有匹配的成员</div>
  }
  return (
    <div role="listbox" aria-label="提及成员">
      {items.map((t, i) => (
        <div
          key={t.id}
          role="option"
          aria-selected={i === idx}
          className={`c-mention-suggest__item${i === idx ? ' is-on' : ''}`}
          onMouseDown={(e) => {
            e.preventDefault()
            command(t)
          }}
          onMouseEnter={() => setIdx(i)}
        >
          <span
            style={{
              width: 16,
              height: 16,
              borderRadius: 'var(--radius-sm)',
              background: t.color,
              display: 'grid',
              placeItems: 'center',
              fontSize: 'var(--text-xs)',
              fontWeight: 700,
              color: 'var(--bg-base)',
              flexShrink: 0,
            }}
          >
            {t.name.slice(0, 1).toUpperCase()}
          </span>
          <span>{t.name}</span>
        </div>
      ))}
    </div>
  )
}

/** suggestion 弹层：fixed 定位在光标下方，键盘事件转发给列表。 */
function suggestionRenderer() {
  let root: Root | null = null
  let el: HTMLDivElement | null = null
  let keyHandler: ((e: KeyboardEvent) => boolean) | null = null
  const register = (h: (e: KeyboardEvent) => boolean) => {
    keyHandler = h
  }
  const place = (rect?: (() => DOMRect | null) | null) => {
    const r = rect?.()
    if (!el || !r) return
    el.style.left = `${Math.min(r.left, window.innerWidth - 260)}px`
    // 输入框在底部，弹层开在光标上方，避免被视口下沿裁掉滑不动
    el.style.top = 'auto'
    el.style.bottom = `${window.innerHeight - r.top + 6}px`
    const inner = el.firstElementChild as HTMLElement | null
    if (inner) inner.style.maxHeight = `${Math.min(320, Math.max(160, r.top - 16))}px`
  }
  const draw = (props: SuggestionProps<Target, Target>) => {
    root?.render(
      <div className="c-mention-suggest">
        <SuggestList state={{ items: props.items, command: props.command }} register={register} />
      </div>,
    )
    place(props.clientRect)
  }
  return {
    onStart: (props: SuggestionProps<Target, Target>) => {
      el = document.createElement('div')
      el.style.position = 'fixed'
      el.style.zIndex = '120'
      document.body.appendChild(el)
      root = createRoot(el)
      draw(props)
    },
    onUpdate: draw,
    onKeyDown: (props: SuggestionKeyDownProps) => keyHandler?.(props.event) ?? false,
    onExit: () => {
      root?.unmount()
      el?.remove()
      root = null
      el = null
      keyHandler = null
    },
  }
}

/**
 * MentionInput —— 行内 @ 提及输入框（TipTap Mention）。
 * 胶囊是原子节点：Backspace 整体删除；选中自动补一个后置空格；
 * 一句话里可以有多个 @。Enter 发送、Shift+Enter 换行。
 */
const MentionInput = forwardRef<
  MentionInputHandle,
  {
    text: string
    onChange: (text: string, mentions: string[]) => void
    targets: Target[]
    placeholder: string
    onSubmit: () => void
    onPasteFiles?: (files: File[]) => void
    disabled?: boolean
  }
>(function MentionInput({ text, onChange, targets, placeholder, onSubmit, onPasteFiles, disabled }, ref) {
  const targetsRef = useRef(targets)
  targetsRef.current = targets
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const onSubmitRef = useRef(onSubmit)
  onSubmitRef.current = onSubmit
  const onPasteRef = useRef(onPasteFiles)
  onPasteRef.current = onPasteFiles

  const editor = useEditor({
    extensions: [
      StarterKit.configure({
        heading: false,
        blockquote: false,
        bulletList: false,
        orderedList: false,
        listItem: false,
        listKeymap: false,
        codeBlock: false,
        code: false,
        horizontalRule: false,
        bold: false,
        italic: false,
        strike: false,
        underline: false,
        link: false,
      }),
      // Enter 发送。插件顺序＝同优先级下数组倒序：必须排在 Mention 之前，
      // 这样 suggestion（弹层开着时接管 Enter 选人）先于本快捷键，
      // 本快捷键又先于 core Keymap 的 Enter 分段。
      Extension.create({
        name: 'submitKey',
        addKeyboardShortcuts() {
          return {
            Enter: () => {
              onSubmitRef.current()
              return true
            },
          }
        },
      }),
      Mention.configure({
        HTMLAttributes: { class: 'c-mention-chip' },
        renderText: ({ node }) => `@${node.attrs.label ?? node.attrs.id}`,
        renderHTML: ({ node }) => [
          'span',
          {
            'data-type': 'mention',
            'data-id': node.attrs.id,
            'data-label': node.attrs.label,
            class: 'c-mention-chip',
          },
          `@${node.attrs.label ?? node.attrs.id}`,
        ],
        deleteTriggerWithBackspace: true,
        suggestion: {
          char: '@',
          // 句首/空白/中英文标点后触发；邮箱、代码里的 abc@ 不触发
          allowedPrefixes: [' ', '\n', '，', '。', '、', '；', '：', '（', ',', ';', ':', '('],
          items: ({ query }) =>
            targetsRef.current.filter(
              (t) =>
                t.name.toLowerCase().includes(query.toLowerCase()) ||
                t.id.toLowerCase().includes(query.toLowerCase()),
            ),
          command: ({ editor: ed, range, props }) => {
            ed.chain()
              .focus()
              .insertContentAt(range, [
                { type: 'mention', attrs: { id: props.id, label: props.name } },
                { type: 'text', text: ' ' },
              ])
              .run()
          },
          render: suggestionRenderer,
        },
      }),
      Placeholder.configure({ placeholder }),
    ],
    editorProps: {
      attributes: {
        class: 'c-mention-editor',
        role: 'textbox',
        'aria-label': placeholder,
        'aria-multiline': 'true',
      },
      handlePaste: (_view, event) => {
        const files = Array.from(event.clipboardData?.files ?? [])
        if (files.length > 0 && onPasteRef.current) {
          onPasteRef.current(files)
          return true
        }
        return false
      },
    },
    onUpdate: ({ editor: ed }) => {
      const { text: t, mentions } = serialize(ed)
      onChangeRef.current(t, mentions)
    },
  })

  useImperativeHandle(ref, () => ({
    focus: () => editor?.commands.focus(),
  }))

  // 外部文本（回填 / 发送后清空）与编辑器同步；认识的 @名字 还原成胶囊
  useEffect(() => {
    if (!editor) return
    if (serialize(editor).text !== text) {
      editor.commands.setContent(toHTML(text, targetsRef.current), { emitUpdate: false })
    }
  }, [text, editor])

  useEffect(() => {
    editor?.setEditable(!disabled)
  }, [editor, disabled])

  return <EditorContent editor={editor} />
})

export default MentionInput