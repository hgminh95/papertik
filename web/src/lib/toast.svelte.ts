// One short message at a time, shown at the bottom of the screen.
export const toast = $state({ message: '', id: 0 })

let timer: ReturnType<typeof setTimeout>

export function showToast(message: string, ms = 2600) {
  toast.message = message
  toast.id++
  clearTimeout(timer)
  timer = setTimeout(() => (toast.message = ''), ms)
}
