import type { AppSnapshot } from './types'

/** How many distinct speaker colours styles.css defines. */
const TONES = 4

/**
 * Colour slot for one audio input. It is keyed on the registered audio sources
 * rather than on the running session, so a speaker keeps the same colour in the
 * caption window, in the history window and after recognition has stopped.
 */
export const speakerTone = (snapshot: AppSnapshot, channelKey?: string): string => {
  const index = channelKey ? snapshot.audioSources.findIndex((item) => item.key === channelKey) : -1
  return `var(--speaker-${(index < 0 ? 0 : index % TONES) + 1})`
}

/** Inline style that binds one element's speaker colour. */
export const speakerStyle = (snapshot: AppSnapshot, channelKey?: string) => ({
  '--speaker': speakerTone(snapshot, channelKey),
})
