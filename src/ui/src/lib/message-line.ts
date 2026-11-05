// How a chat message reads in a list or a bubble, for every archived messenger (WhatsApp, Telegram).

const KIND: Record<string, string> = {
  image: 'Photo',
  photo: 'Photo',
  video: 'Video',
  audio: 'Audio',
  voice: 'Voice message',
  sticker: 'Sticker',
  location: 'Location',
  contact: 'Contact',
  poll: 'Poll',
  other: 'Message',
}

/** The text, or the media label with its caption. */
export function messageLine(kind: string, text: string, fileName?: string): string {
  if (kind === 'text') return text
  const label = kind === 'document' ? `Document${fileName ? `: ${fileName}` : ''}` : (KIND[kind] ?? 'Message')
  return text ? `${label} · ${text}` : label
}
