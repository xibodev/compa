import { cn } from "@/lib/utils"

/** The product name. A brand, so it is the same in every language. */
export const BRAND_NAME = "Compa"

interface BrandLogoProps {
  /** Show the product name beside the mark. */
  withName?: boolean
  /** The mark adds nothing a nearby text does not say already. */
  decorative?: boolean
  className?: string
  markClassName?: string
  nameClassName?: string
}

// The mark's fixed geometry, exactly as brand/tokens.json defines it. The brand
// rules forbid redrawing, rounding, stroking, or closing it; copy, never edit.
const MARK_OUTER =
  "M72 20 H36 C27.16 20 20 27.16 20 36 V64 C20 72.84 27.16 80 36 80 H72 V68 H38 C34.69 68 32 65.31 32 62 V38 C32 34.69 34.69 32 38 32 H72 Z"
const MARK_INNER =
  "M66 38 H46 C41.58 38 38 41.58 38 46 V54 C38 58.42 41.58 62 46 62 H66 V53 H48 C46.9 53 46 52.1 46 51 V49 C46 47.9 46.9 47 48 47 H66 Z"

/**
 * The Compa mark with the product name, from the brand kit in brand/.
 *
 * The mark is inline SVG so it follows the app's theme, not the system's: the
 * kit's primary mark on the light theme and its inverse mark on the dark one.
 * An <img> could do neither, since it cannot see the theme class.
 *
 * The name is the kit's wordmark: Inter at weight 650, drawn lowercase. The
 * text itself stays "Compa", so assistive technology and search read the
 * proper name.
 */
export function BrandLogo({
  withName = true,
  decorative = false,
  className,
  markClassName,
  nameClassName,
}: BrandLogoProps) {
  const labelled = !withName && !decorative
  return (
    <span className={cn("inline-flex items-center gap-0.5", className)}>
      <svg
        viewBox="0 0 100 100"
        role={labelled ? "img" : undefined}
        aria-label={labelled ? BRAND_NAME : undefined}
        aria-hidden={labelled ? undefined : true}
        className={cn("size-7 shrink-0", markClassName)}
      >
        <path d={MARK_OUTER} className="fill-[#3D63D8] dark:fill-[#9CABFF]" />
        <path
          d={MARK_INNER}
          opacity={0.48}
          className="fill-[#2749AD] dark:fill-[#E8ECFF]"
        />
        <rect
          x={72}
          y={44}
          width={12}
          height={12}
          rx={2.5}
          className="fill-[#2749AD] dark:fill-[#E8ECFF]"
        />
      </svg>
      {withName && (
        <span
          className={cn(
            "font-[family-name:'Inter_Variable',Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,'Segoe_UI',sans-serif] text-lg font-[650] tracking-[-0.04em] text-[#171719] lowercase dark:text-[#E8ECFF]",
            nameClassName,
          )}
        >
          {BRAND_NAME}
        </span>
      )}
    </span>
  )
}
