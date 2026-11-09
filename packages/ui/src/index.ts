// OpenFox 设计系统：导出基础组件、图标与样式工具。
export { cn } from "./cn";
export {
  PromptInput,
  PromptInputTextarea,
  PromptInputActions,
} from "./ai/prompt-input";
export { Message, MessageContent, MessageActions } from "./ai/message";
export { CodeBlock, CodeBlockGroup } from "./ai/code-block";
export {
  ChainOfThought,
  ChainOfThoughtContent,
  ChainOfThoughtTrigger,
} from "./ai/chain-of-thought";
export { iconData, multicolorIcons } from "./icons/data";
export { Icon, type IconName, type IconProps } from "./components/Icon";
export { Spinner } from "./components/Spinner";
export {
  Button,
  type ButtonProps,
  type ButtonSize,
  type ButtonVariant,
} from "./components/Button";
export { IconButton, type IconButtonProps } from "./components/IconButton";
export { Badge, type BadgeProps, type BadgeTone } from "./components/Badge";
export { Field, useFieldContext, type FieldProps } from "./components/Field";
export {
  TextInput,
  type TextInputProps,
  type ControlStyle,
} from "./components/TextInput";
export { Textarea, type TextareaProps } from "./components/Textarea";
export { Checkbox, type CheckboxProps } from "./components/Checkbox";
export { Radio } from "./components/Radio";
export { Switch, type SwitchProps } from "./components/Switch";
export {
  Popover,
  type PopoverProps,
  type Placement,
} from "./components/Popover";
export {
  Menu,
  MenuItem,
  MenuLabel,
  MenuSeparator,
  handleMenuKeyDown,
  type MenuItemProps,
  type MenuProps,
} from "./components/Menu";
export {
  Select,
  type SelectOption,
  type SelectProps,
} from "./components/Select";
export {
  Dialog,
  ConfirmDialog,
  useModalBehavior,
  type DialogProps,
  type ConfirmDialogProps,
} from "./components/Dialog";
export { Drawer, type DrawerProps } from "./components/Drawer";
export {
  ToastProvider,
  useToast,
  type ToastOptions,
  type ToastTone,
} from "./components/Toast";
export {
  Avatar,
  avatarColor,
  initialOf,
  type AvatarProps,
  type AvatarSize,
} from "./components/Avatar";
export { Tooltip } from "./components/Tooltip";
export { Callout, type CalloutTone } from "./components/Callout";
export { Skeleton } from "./components/Skeleton";
export { Stepper } from "./components/Stepper";
export { OtpInput, type OtpInputProps } from "./components/OtpInput";
export { Segmented, type SegmentedOption } from "./components/Segmented";
export { Calendar, type CalendarProps } from "./components/Calendar";
export {
  DateFieldPopover,
  type DateFieldPopoverProps,
} from "./components/DateFieldPopover";
export {
  SearchMenu,
  type SearchMenuGroup,
  type SearchMenuOption,
  type SearchMenuProps,
} from "./components/SearchMenu";
export {
  ColorSwatches,
  LABEL_COLORS,
  OPTION_COLORS,
  STATUS_LIFECYCLE_COLORS,
  type ColorSwatchesProps,
} from "./components/ColorSwatches";
export { Kbd } from "./components/Kbd";
export { Tabs, type TabItem, type TabsProps } from "./components/Tabs";
export {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
  type TableHeaderCellProps,
  type TableRowProps,
} from "./components/Table";
export {
  addDays,
  addMonths,
  formatDateLabel,
  parseYmd,
  quickDates,
  todayYmd,
  type QuickDates,
} from "./date";
