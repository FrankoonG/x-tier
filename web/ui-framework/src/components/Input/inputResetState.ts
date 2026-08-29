export interface FormResetInput {
  readonly form: EventTarget | null;
  readonly value: string;
}

/** Reads the browser's reset value after the native default action has run. */
export function observeFormReset(
  input: FormResetInput,
  onFilledChange: (filled: boolean) => void,
): () => void {
  const form = input.form;
  if (!form) return () => undefined;

  let timer: ReturnType<typeof setTimeout> | null = null;
  const handleReset = () => {
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      onFilledChange(input.value.length > 0);
    }, 0);
  };

  form.addEventListener('reset', handleReset);
  return () => {
    form.removeEventListener('reset', handleReset);
    if (timer !== null) clearTimeout(timer);
  };
}
