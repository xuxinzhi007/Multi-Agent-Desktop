import { useEffect, useState } from 'react'
import { API, type Block } from '../mygo'

export type DraftImage = { mime: string; data: string; url: string }

const MAX = 8
const MAX_BYTES = 8 << 20

export const blindSentence = '这个会话还不能看图'

export async function readImageFiles(list: File[]): Promise<{ images: DraftImage[]; error: string }> {
  const images: DraftImage[] = []
  for (const file of list) {
    if (!file.type.startsWith('image/')) continue
    if (file.size > MAX_BYTES) return { images, error: '单张图超过 8MB' }
    if (images.length >= MAX) return { images, error: '一次最多 8 张图' }
    const data = await fileToBase64(file)
    images.push({ mime: file.type, data, url: URL.createObjectURL(file) })
  }
  if (images.length === 0 && list.length > 0) return { images, error: '只接收 png、jpeg、gif、webp' }
  return { images, error: '' }
}

function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const url = String(reader.result || '')
      const at = url.indexOf(',')
      resolve(at >= 0 ? url.slice(at + 1) : url)
    }
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })
}

export function ImageStrip({ images, onRemove }: { images: DraftImage[]; onRemove?: (i: number) => void }) {
  if (images.length === 0) return null
  return (
    <>
      <div className="c-image__telemetry">
        <span>{images.length}/{MAX}</span>
      </div>
      <div className="c-image__strip">
        {images.map((img, i) => (
          <ImageThumb key={img.url} img={img} onRemove={onRemove ? () => onRemove(i) : undefined} />
        ))}
      </div>
    </>
  )
}

function ImageThumb({ img, onRemove }: { img: DraftImage; onRemove?: () => void }) {
  const [dimensions, setDimensions] = useState('')
  const kb = Math.max(1, Math.round(img.data.length * 3 / 4 / 1024))
  return (
    <span className="c-image__item" style={{ position: 'relative' }}>
      <img
        src={img.url}
        alt=""
        style={{ height: 72, maxWidth: 140, objectFit: 'cover', borderRadius: 8, display: 'block' }}
        onLoad={(e) => setDimensions(`${e.currentTarget.naturalWidth}×${e.currentTarget.naturalHeight}`)}
        title={`${dimensions || '尺寸读取中'} · 约 ${kb}KB · ${img.mime}`}
      />
      {onRemove && (
        <button type="button" aria-label="移除图片" onClick={onRemove} className="c-image__remove" style={{ position: 'absolute', top: 2, right: 2 }}>
          ×
        </button>
      )}
    </span>
  )
}

export function MessageShots({ blocks, content }: { blocks?: Block[]; content: string }) {
  const list = blocks ?? []
  const hasText = list.some((b) => b.text)
  const shots = list.filter((b) => b.id)
  if (hasText) {
    return (
      <div>
        {list.map((b, i) =>
          b.text ? (
            <div key={i} style={{ whiteSpace: 'pre-wrap' }}>
              {b.text}
            </div>
          ) : (
            <Shot key={b.id || i} block={b} />
          ),
        )}
      </div>
    )
  }
  return (
    <div>
      {shots.map((b, i) => (
        <Shot key={b.id || i} block={b} />
      ))}
      {content ? <div style={{ whiteSpace: 'pre-wrap' }}>{content}</div> : null}
    </div>
  )
}

function Shot({ block }: { block: Block }) {
  const [url, setUrl] = useState('')
  useEffect(() => {
    if (!block.id) return
    let stop = false
    API.imageData(block.id)
      .then((file) => {
        if (!stop && file?.data) setUrl(`data:${file.mime || block.mime || 'image/png'};base64,${file.data}`)
      })
      .catch(() => {
        if (!stop) setUrl('')
      })
    return () => {
      stop = true
    }
  }, [block.id, block.mime])
  if (!url) return <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}>图没拿到</div>
  return <img src={url} alt="" style={{ maxWidth: '100%', maxHeight: 320, borderRadius: 8, display: 'block', margin: '4px 0' }} />
}
