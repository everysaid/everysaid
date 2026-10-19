import { useEffect } from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { useTranslation } from "react-i18next";
import { ChevronLeft, ChevronRight, X } from "lucide-react";

export interface ViewerImage {
  src: string;
  label?: string;
}

/** Pictures shown whole on the screen, one at a time with its label, the arrows to the others: a
 *  person's photos from each source, a group's photo. */
export function ImageViewer({ images, index, onIndex, onClose }: {
  images: ViewerImage[];
  index: number | null;
  onIndex: (i: number) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  useEffect(() => {
    if (index == null) return;
    const key = (e: KeyboardEvent) => {
      if (e.key === "ArrowLeft" && index > 0) onIndex(index - 1);
      if (e.key === "ArrowRight" && index < images.length - 1) onIndex(index + 1);
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [index, images.length, onIndex]);

  const image = index != null ? images[index] : null;
  if (!image) return null;
  return (
    <DialogPrimitive.Root open onOpenChange={(o) => !o && onClose()}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/95" />
        <DialogPrimitive.Content
          className="fixed inset-0 z-50 flex flex-col text-white outline-none"
          aria-describedby={undefined}
          data-image-viewer
          // a click on the black around the picture closes it; one on the picture or a button does not
          onClick={(e) => !(e.target as HTMLElement).closest("button, img") && onClose()}
        >
          <div className="flex items-center gap-1 p-2 pt-[max(0.5rem,env(safe-area-inset-top))]">
            <button onClick={onClose} className="grid size-11 place-items-center rounded-full hover:bg-white/10" aria-label={t("common.close")}><X /></button>
            <DialogPrimitive.Title className="min-w-0 flex-1 truncate px-2 text-sm opacity-80">{image.label || t("kind.image")}</DialogPrimitive.Title>
          </div>
          <div className="relative flex min-h-0 flex-1 items-center justify-center px-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]">
            <img key={image.src} src={image.src} alt="" className="max-h-full max-w-full rounded-lg object-contain" />
            {index! > 0 && (
              <button onClick={() => onIndex(index! - 1)} className="absolute left-2 grid size-12 place-items-center rounded-full bg-white/10 hover:bg-white/20" aria-label={t("common.previous")}><ChevronLeft /></button>
            )}
            {index! < images.length - 1 && (
              <button onClick={() => onIndex(index! + 1)} className="absolute right-2 grid size-12 place-items-center rounded-full bg-white/10 hover:bg-white/20" aria-label={t("common.next")}><ChevronRight /></button>
            )}
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
