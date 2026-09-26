export interface GroupedSelectOption {
  readonly label: string
  readonly value: string
  readonly disabled?: boolean
}

export interface GroupedSelectGroup {
  readonly label?: string
  readonly options: readonly GroupedSelectOption[]
}
